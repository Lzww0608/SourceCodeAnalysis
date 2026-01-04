## Poll.go

```go
package netpoll

// Poll monitors fd(file descriptor), calls the FDOperator to perform specific actions,
// and shields underlying differences. On linux systems, poll uses epoll by default,
// and kevent by default on bsd systems.
type Poll interface {
	// Wait will poll all registered fds, and schedule processing based on the triggered event.
	// The call will block, so the usage can be like:
	//
	//  go wait()
	//
	Wait() error

	// Close the poll and shutdown Wait().
	Close() error

	// Trigger can be used to actively refresh the loop where Wait is located when no event is triggered.
	// On linux systems, eventfd is used by default, and kevent by default on bsd systems.
	Trigger() error

	// Control the event of file descriptor and the operations is defined by PollEvent.
	Control(operator *FDOperator, event PollEvent) error

	// Alloc the operator from cache.
	Alloc() (operator *FDOperator)

	// Free the operator from cache.
	Free(operator *FDOperator)
}

// PollEvent defines the operation of poll.Control.
type PollEvent int

const (
	// PollReadable is used to monitor whether the FDOperator registered by
	// listener and connection is readable or closed.
	PollReadable PollEvent = 0x1

	// PollWritable is used to monitor whether the FDOperator created by the dialer is writable or closed.
	// ET mode must be used (still need to poll hup after being writable)
	PollWritable PollEvent = 0x2

	// PollDetach is used to remove the FDOperator from poll.
	PollDetach PollEvent = 0x3

	// PollR2RW is used to monitor writable for FDOperator,
	// which is only called when the socket write buffer is full.
	PollR2RW PollEvent = 0x5

	// PollRW2R is used to remove the writable monitor of FDOperator, generally used with PollR2RW.
	PollRW2R PollEvent = 0x6
)
```



## Wait() error

它会调用底层的**阻塞等待**函数（如 `epoll_wait`）。每个 Poller 实例独占一个 Goroutine。这与 Go 原生 `net` 库（一个全局 `Poller` 调度所有协程）不同，`Netpoll` 采用的是 **Multi-Reactor** 模型（多个 `Poller` 并行工作）。



## Trigger() error

`Wait()` 通常是阻塞的。如果另一个 Goroutine 想要立刻执行某些操作（比如注册一个新的连接），它不能傻傻地等 `Wait` 超时返回。`Trigger` 实现了**“从外部唤醒 Poller”**的机制。Linux 下通常使用 `eventfd`。



## Control(operator *FDOperator, event PollEvent) error

相当于 `epoll_ctl` 的封装。用于增删改对某个 FD (文件描述符) 的监听状态。**参数 FDOperator**: 注意这里传的不是裸的 `int` 类型的 fd，而是一个 `FDOperator` 指针。这是一个很关键的设计：**将 FD 和它的回调行为绑定在一起**。当事件发生时，Poller 可以直接拿到这个对象进行回调，避免了额外的 Map 查找开销。



## Alloc() & Free()

`FDOperator` 是高频创建和销毁的对象（每个连接一个）。如果直接 `new`，会给 GC 带来巨大压力。接口内置了对象的申请和释放方法，意味着 Poller 内部维护了 `FDOperator` 的 **对象池 (Object Pool)**。这是 Netpoll 高性能的秘诀之一。



## PollEvent

`PollEvent` 定义了 Poller 对 FD 的监听行为。

**PollReadable (0x1)**: 监听读事件。这是常态，绝大多数连接 99% 的时间都处于这个状态。

**PollWritable (0x2)**: 监听写事件。

- **注意**:  **ET mode (Edge Triggered, 边缘触发)**。Netpoll 默认使用 ET 模式，这意味着只有状态变化时才会通知，性能更高，但对代码编写要求更严（必须一次性读写完直到 EAGAIN）。

**PollR2RW (0x5) & PollRW2R (0x6)**:

- 这是**极致优化**的体现。
- **场景**: 通常我们不监听 Write 事件，因为 Socket 缓冲区大部分时间是空的，监听它是浪费 CPU。只有当我们在 Go 代码中写数据遇到 `EAGAIN`（缓冲区满了）时，我们才需要去监听 Write 事件。
- **R2RW (Read to Read+Write)**: 从只读切换到读写双监听。
- **RW2R (Read+Write to Read)**: 写缓冲区空了，数据写完了，立刻切回只读监听。
- **设计意图**: 减少 `epoll_ctl` 的调用次数和内核态切换，通过定义这种复合状态，明确状态流转路径。

