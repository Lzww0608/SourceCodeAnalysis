# LinkBufferNode

## Definition 

```go
// newLinkBufferNode create or reuse linkBufferNode.
// Nodes with size <= 0 are marked as readonly, which means the node.buf is not allocated by this mcache.
func newLinkBufferNode(size int) *linkBufferNode {
	node := linkedPool.Get().(*linkBufferNode)
	// reset node offset
	node.off, node.malloc, node.refer, node.mode = 0, 0, 1, defaultLinkBufferMode
	if size <= 0 {
		node.setMode(readonlyMask, true)
		return node
	}
	if size < LinkBufferCap {
		size = LinkBufferCap
	}
	node.buf = malloc(0, size)
	return node
}

var linkedPool = sync.Pool{
    // 注意: 这里复用的仅仅是 linkBufferNode 结构体本身（那几十个字节的元数据），而不是它内部的 buf（几KB的字节数组）。buf 的生命周期管理更复杂，通常由 malloc 和 free 单独控制。
	New: func() interface{} {
		return &linkBufferNode{
			refer: 1, // comes with 1 reference
		}
	},
}

type linkBufferNode struct {
	buf    []byte          // buffer: 实际存储数据的字节切片
	off    int             // read-offset: 读偏移量。buf[off:] 是未读数据
	malloc int             // write-offset: 写偏移量。buf[:malloc] 是有效数据
	refer  int32           // reference count: 引用计数
	mode   uint8           // mode: 状态位掩码（如只读、零拷贝读等）
	origin *linkBufferNode // origin: 指向“根”节点。用于支持切片引用的生命周期管理
	next   *linkBufferNode // next: 链表指针，指向下一个节点
}
```



## Next

```go
func (node *linkBufferNode) Next(n int) (p []byte) {
	off := node.off
	node.off += n
	return node.buf[off:node.off:node.off]
}
```

### 核心功能：零拷贝读取

这个方法的作用是“消费”节点中的 n 个字节。

- 它**不拷贝**数据，而是直接基于底层的 node.buf 创建一个新的切片头（Slice Header）。
- 它修改了 node.off，意味着这部分数据被标记为“已读”。

### 关键技巧：三下标切片 (Full Slice Expression)

这是这段代码最值得注意的地方：`node.buf[off : node.off : node.off]`

通常我们切片是写 `buf[low : high]`，但这里用了 `buf[low : high : max]`

- **low (off)**: 切片的起始索引。
- **high (node.off)**: 切片的结束索引（不包含）。切片长度 len = high - low = n。
- **max (node.off)**: **切片的容量限制**。切片容量 cap = max - low = n。



## Refer

```go
func (node *linkBufferNode) Refer(n int) (p *linkBufferNode) {
	p = newLinkBufferNode(0)
	p.buf = node.Next(n)

	if node.origin != nil {
		p.origin = node.origin
	} else {
		p.origin = node
	}
	atomic.AddInt32(&p.origin.refer, 1)
	return p
}
```


这段代码实现了 `LinkBuffer` 高级功能中最核心的部分：**基于引用计数的零拷贝切片（Zero-Copy Slicing with Reference Counting）**。它的主要作用是：从当前节点中“切”出 n 个字节的数据，将其包装成一个新的 `linkBufferNode` 返回。这个新节点不拥有独立的内存，而是共享原节点的内存。

1. **零拷贝**：数据在内存中只有一份，新节点只是指向它。
2. **数据消费**：调用 Refer 会推进原节点的读指针（就像 Read 一样）。
3. **内存安全**：通过引用计数（Reference Counting），确保底层内存只有在所有使用者（包括原始节点和所有切片节点）都释放后才会被回收。
4. **高效管理**：通过 origin 指针直接指向根节点，避免了引用链过长导致的管理复杂性。



## Release

```go
func (node *linkBufferNode) Release() (err error) {
	if node.origin != nil {
		node.origin.Release()
	}
	if atomic.AddInt32(&node.refer, -1) == 0 {
		if node.reusable() {
			free(node.buf)
		}
		node.buf, node.origin, node.next = nil, nil, nil
		linkedPool.Put(node)
	}
	return nil
}
```

### 1. 级联释放 (Cascading Release)

```go
if node.origin != nil {
    node.origin.Release()
}
```

- **场景**: 当前节点 (node) 是通过 Refer 创建出来的“切片节点”。它不拥有底层的 buffer 内存，而是指向一个“根节点” (origin)。
- **逻辑**: 当这个切片节点被释放时，它必须通知根节点：“我不再引用你了”。
- **实现**: 递归调用 node.origin.Release()。
  - 这会导致根节点的引用计数减 1。
  - 如果根节点的计数降为 0，根节点拥有的内存才会被回收。

### 2. 自身引用计数递减 (Self Reference Decrement)

```go
if atomic.AddInt32(&node.refer, -1) == 0 {
    // ... 进入回收逻辑 ...
}
```

- **原子操作**: 使用 atomic 确保线程安全。因为可能有多个 goroutine 同时持有或释放引用。
- **阈值判断**: 只有当引用计数**精确降为 0** 时，才执行后续的销毁逻辑。
  - 对于“根节点”：这意味着所有指向它的切片（Refer 出来的节点）都已经释放了，且根节点自己也释放了。
  - 对于“切片节点”：它的初始引用计数是 1。调用 Release 后变为 0，意味着这个对象本身可以被回收了。