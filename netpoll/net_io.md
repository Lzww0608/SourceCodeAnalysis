这个文件的核心目的是**执行网络I/O（读和写）**。

它不使用 Go 标准库的 `net`或`os`包，而是直接调用`syscall`包。这么做的唯一目的就是**追求极致的性能**，减少 Go 运行时和标准库抽象层带来的开销。



```go
// Copyright 2023 CloudWeGo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build darwin || netbsd || freebsd || openbsd || dragonfly || linux
// +build darwin netbsd freebsd openbsd dragonfly linux

package netpoll

import "syscall"

// return value:
// - n: n == 0 but err == nil, retry syscall
// - err: if not nil, connection should be closed.
/*
ioread 从文件描述符fd中读取数据
*/
func ioread(fd int, bs [][]byte, ivs []syscall.Iovec) (n int, err error) {
	n, err = readv(fd, bs, ivs)
	if n == 0 && err == nil { // means EOF
		return 0, Exception(ErrEOF, "")
	}
	if err == syscall.EINTR || err == syscall.EAGAIN {
		return 0, nil
	}
	return n, err
}

// return value:
// - n: n == 0 but err == nil, retry syscall
// - err: if not nil, connection should be closed.
func iosend(fd int, bs [][]byte, ivs []syscall.Iovec, zerocopy bool) (n int, err error) {
	n, err = sendmsg(fd, bs, ivs, zerocopy)
	if err == syscall.EAGAIN {
		return 0, nil
	}
	return n, err
}
```



#### `func ioread(...)`

这个函数用于从一个文件描述符（`fd`）中**读取**数据。

- **关键调用**: `n, err = readv(fd, bs, ivs)`
  - `readv` 是一个 `syscall`（系统调用）的封装。`v` 代表 **"vector"（向量）**。
  - 普通的 `read` 系统调用只能读到一个 buffer (`[]byte`) 中。
  - `readv`（向量化读取）可以**一次性将数据“分散（Scatter）”读到多个 buffer** 中（由 `ivs []syscall.Iovec` 定义）。`bs` 则是这些 `Iovec` 对应的 Go 切片，防止它们被 GC。
  - **优势**: 只需要一次系统调用，就可以把内核缓冲区的数据填充到多个不同的 Go buffer 中，极大地减少了系统调用的次数和上下文切换的开销。
- **错误处理 (非常关键)**:
  1. `if n == 0 && err == nil`: 在 Unix 系统中，`read` 调用成功返回0字节，意味着**对端关闭了连接（EOF, End Of File）**。这里将其封装为自定义的 `ErrEOF` 错误。
  2. `if err == syscall.EINTR`: `EINTR` (Interrupted System Call) 意味着系统调用被一个外部信号（Signal）中断了。**这不是一个真正的错误**。函数返回 `(0, nil)`，通知上层（`netpoll` 循环）“什么也没发生，请重试”。
  3. `if err == syscall.EAGAIN`: `EAGAIN` (Resource Temporarily Unavailable) 或 `EWOULDBLOCK`。这是**非阻塞I/O的核心**。它意味着 `fd` 被设置为了非阻塞模式，但**当前没有数据可读**。**这也不是一个错误**。函数同样返回 `(0, nil)`，告诉 `netpoll`：“现在没数据，等下次轮询器通知可读时再来试”。
  4. `return n, err`: 其他任何错误（例如 `EBADF` - 坏的文件描述符）都被视为**连接级别的致命错误**，应返回给上层，上层通常会关闭这个连接。





#### `func iosend(...)`

这个函数用于向一个文件描述符（`fd`）**发送**数据。

- **关键调用**: `n, err = sendmsg(fd, bs, ivs, zerocopy)`
  - `sendmsg` 是 `readv` 的对应方。它是一个 `syscall`（系统调用）的封装。
  - 它（向量化写入）可以**一次性将多个 buffer**（由 `ivs` 定义）中的数据“聚集（Gather）”起来，通过一次系统调用全部发送出去。
  - **优势**: 同样是避免了多次系统调用。例如，你需要发送一个 HTTP 响应，它由 "Header 1", "Header 2", "Body" 三块独立的 `[]byte` 组成，`sendmsg` 可以将它们一次性发送，而不需要先在 Go 里把它们拼接成一个大的 `[]byte`。
  - `zerocopy bool`: 这个参数表示 `sendmsg` 的封装函数可能会尝试使用**零拷贝**技术（如 Linux 的 `sendfile` 或 `MSG_ZEROCOPY` 标志），这可以避免数据在内核空间和用户空间之间的不必要拷贝，进一步提升性能。
- **错误处理**:
  1. `if err == syscall.EAGAIN`: 与 `ioread` 类似，这在非阻塞 I/O 中意味着**内核的发送缓冲区已经满了**。**这不是错误**。函数返回 `(0, nil)`，告诉 `netpoll`：“现在发不出去，等下次轮询器通知可写时再来试”。
  2. `return n, err`: 其他错误（如连接被对端重置 `ECONNRESET`）被视为致命错误返回。