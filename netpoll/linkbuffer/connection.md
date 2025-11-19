# connection interface



## Bytes

```go
func (b *UnsafeLinkBuffer) Bytes() []byte {
	node, flush := b.read, b.flush
	if node == flush {
		return node.buf[node.off:]
	}
	n := 0
	p := dirtmake.Bytes(b.Len(), b.Len())
	for ; node != flush; node = node.next {
		if node.Len() > 0 {
			n += copy(p[n:], node.buf[node.off:])
		}
	}
	n += copy(p[n:], flush.buf[flush.off:])
	return p[:n]
}
```

### 1. 快照状态

```go
node, flush := b.read, b.flush
```

首先获取当前的读节点指针 (read) 和刷新节点指针 (flush)。

- read 是数据的开始。
- flush 是数据的结束边界（注意：flush 节点本身可能包含数据，但 flush 指针之后的节点是未提交的内存）。

### 2. 快路径：零拷贝 (Zero-Copy)

```go
if node == flush {
    return node.buf[node.off:]
}
```

这是性能优化的核心。

- **条件**: `node == flush` 意味着**所有可读数据都位于同一个内存块（节点）中**。
- **操作**: 直接返回该节点内部 buf 的切片。
- **优势**: **零拷贝**，**零分配**。不需要创建新的大数组，也不需要搬运数据，仅仅是创建了一个切片头，速度极快。
- **注意**: 返回的切片与 `LinkBuffer` 共享底层内存。如果 `LinkBuffer` 之后被释放或重用，这个切片的数据可能会变脏（虽然 `LinkBuffer` 的设计要求用户负责生命周期，但这一点仍需注意）。

### 3. 慢路径：内存分配与拷贝 (Allocation & Copy)

如果数据跨越了多个节点（node != flush），就无法返回单一的切片视图了（因为底层内存不连续）。必须将它们拼接到一个新的连续内存块中。

#### A. 分配内存

```go
n := 0
p := dirtmake.Bytes(b.Len(), b.Len())
```

- b.Len(): 获取当前所有可读数据的总长度。
- dirtmake.Bytes: 分配一块大小为总长度的连续内存 p。
  - 这里使用 `dirtmake` 是为了**避免内存零初始化**（Zero-initialization）。因为我们马上就要用 copy 把这块内存填满，所以让 Go 运行时先把内存清零是浪费 CPU 资源的。这是一个底层的性能优化。

#### B. 循环拷贝（处理中间节点）

```go
for ; node != flush; node = node.next {
    if node.Len() > 0 {
        n += copy(p[n:], node.buf[node.off:])
    }
}
```

- 遍历从 read 开始的链表，直到遇到 flush 节点之前。
- 将每个节点的数据拷贝到大数组 p 的对应位置。
- n 作为游标，记录当前拷贝到了哪里。

#### C. 尾部拷贝（处理 flush 节点）

```go
n += copy(p[n:], flush.buf[flush.off:])
return p[:n]
```

- 循环条件是 node != flush，所以循环结束时，flush 节点的数据还没拷。
- 这里单独处理 flush 节点的数据拷贝。
- 最后返回填满数据的切片 p。



## GetBytes

```go
func (b *UnsafeLinkBuffer) GetBytes(p [][]byte) (vs [][]byte) {
	node, flush := b.read, b.flush
	if len(p) == 0 {
		n := 0
		for ; node != flush; node = node.next {
			n++
		}
		node = b.read
		p = make([][]byte, n)
	}
	var i int
	for i = 0; node != flush && i < len(p); node = node.next {
		if node.Len() > 0 {
			p[i] = node.buf[node.off:]
			i++
		}
	}
	if i < len(p) {
		p[i] = flush.buf[flush.off:]
		i++
	}
	return p[:i]
}
```

它的核心功能是：**以零拷贝的方式，获取缓冲区中所有不连续的内存块切片（Vectorized View）。**这通常用于 **Vectorized I/O**（向量化 I/O）操作，例如 `writev` 系统调用。`writev` 允许你一次性将多个不连续的内存块写入文件描述符，而不需要先将它们拼接到一个大的连续 buffer 中。

### 1. 自动分配逻辑 (Auto-Allocation)

```
if len(p) == 0 {
    n := 0
    for ; node != flush; node = node.next {
       n++
	}
	node = b.read // 重置 node 指针回起点
	p = make([][]byte, n)
}
```

当用户没有提供容器 p 时，代码尝试自动计算需要的长度。

- 它遍历从 read 到 flush 之间的节点数 n。
- **潜在逻辑问题**: 注意这里的循环条件是 `node != flush`。这意味着它统计的是 flush 节点**之前**的节点数量。

### 2. 填充逻辑 (Filling Loop)

```go
var i int
for i = 0; node != flush && i < len(p); node = node.next {
    if node.Len() > 0 {
       p[i] = node.buf[node.off:] // 零拷贝引用
       i++
    }
}
```

- **双重条件**: node != flush (遍历链表) 和 i < len(p) (防止越界，遵循用户传入切片的容量限制)。
- **过滤空节点**: if node.Len() > 0。只有包含数据的节点才会被放入结果集。
- **零拷贝**: p[i] = node.buf[...] 直接引用底层内存，没有发生数据复制。

### 3. 尾部节点处理 (Flush Node)

```go
if i < len(p) {
   p[i] = flush.buf[flush.off:]
   i++
}
return p[:i]
```

- 循环结束后，处理 flush 指针指向的那个节点。
- **关键检查**: if i < len(p)。只有当结果切片 p 还有剩余空间时，才会把 flush 节点的数据放进去。

## book

```go
func (b *UnsafeLinkBuffer) book(bookSize, maxSize int) (p []byte) {
	l := cap(b.write.buf) - b.write.malloc
	if l == 0 {
		l = maxSize
		b.write.next = newLinkBufferNode(maxSize)
		b.write = b.write.next
	}
	if l > bookSize {
		l = bookSize
	}
	return b.write.Malloc(l)
}
```

**为即将到来的写入操作“预订”或“分配”一块内存区域。**

#### 扩容逻辑 (Growth Strategy)

```go
if l == 0 {
    l = maxSize
    b.write.next = newLinkBufferNode(maxSize)
    b.write = b.write.next
}
```

如果当前节点**完全满了** (l == 0)，必须分配新节点。

- **关键优化**: 这里使用的是 `maxSize` 而不是 `bookSize` 来创建新节点。
- **场景**: 假设你在读取一个网络包。你知道包的最大长度可能是 4KB (maxSize)。现在你只需要读取头部 10 字节 (bookSize)。
  - 如果没有这个优化，可能只会分配一个 10 字节的小节点。
  - 有了这个优化，book 会直接分配一个 `4KB` 的大节点。虽然这次只用 10 字节，但接下来的包体数据可以直接写入这个大节点的剩余空间，从而保证了**数据的连续性**，避免了将一个包拆分到多个小节点中。这对于后续的零拷贝读取（Next）至关重要。

#### 确定返回切片的大小

```go
if l > bookSize {
    l = bookSize
}
```

这里处理了两种情况：

- **情况 A (空间充足)**: 如果剩余空间 l 大于请求的 `bookSize`，我们只给调用者它请求的大小。剩下的空间留给下一次 book 调用。
- **情况 B (空间不足)**: 如果当前节点有剩余空间，但小于 `bookSize`（例如当前剩 50 字节，你想写 100 字节）。
  - 注意：代码**不会**在这种情况下触发扩容。
  - 它会直接跳过这个 if，保持 l 为当前剩余空间（比如 50）。
  - 这意味着 book 返回的切片长度可能**小于**请求的 `bookSize`。

## bookAck

```go
func (b *UnsafeLinkBuffer) bookAck(n int) (length int, err error) {
	b.write.malloc = n + len(b.write.buf)
	b.write.buf = b.write.buf[:b.write.malloc]
	b.flush = b.write

	length = b.recalLen(n)
	return length, nil
}
```

它的作用是：**确认（Acknowledge）实际写入的数据量，并将这些数据立即提交为“可读”状态，同时丢弃多余的预订空间。**它是 book 方法的配套操作。book 负责“预订”最大可能的空间，而 `bookAck` 负责“结算”实际使用的空间。

#### 1. 修正写偏移量 (Recalculate Malloc)

```go
b.write.malloc = n + len(b.write.buf)
```

- **len(b.write.buf)**: 这是在调用 book 之前，该节点中已有的有效数据长度。
- **n**: 这是用户实际写入的新数据长度。
- **逻辑**: 新的 malloc 位置应该是 旧数据终点 + 新数据长度。
- **作用**:
  - 如果在 book 中我们预订了 4KB（malloc 增加了 4096），但实际上只写了 100 字节（n=100）。
  - 这行代码会将 malloc 指针往回拉，只保留这 100 字节的增量。
  - **关键点**: 剩余的 4096 - 100 字节的空间被有效地“归还”了，下一次调用 book 或 Malloc 时可以继续使用这块空间，避免了内存浪费。

#### 2. 提交数据 (Commit/Flush Node)

```go
b.write.buf = b.write.buf[:b.write.malloc]
```

- 这行代码调整了切片的长度。
- 它将 `buf` 的长度扩展到了 `malloc` 的位置。
- 这意味着这 n 字节的数据现在正式成为了节点 `buf` 的一部分，对于底层的切片访问来说是可见的了。

#### 3. 更新全局指针 (Update Flush Pointer)

```go
b.flush = b.write
```

- 将 flush 指针移动到当前的 write 节点。
- **含义**: 标记数据为**全局可读**。在 LinkBuffer 的状态机中，flush 指针之前的节点（包含 flush 节点本身）被认为是可供 read 指针读取的。
- **区别**: 标准的 `Malloc -> Flush` 流程中，Flush 是单独调用的。而 `book -> bookAck` 流程中，`bookAc`k 隐式地包含了 Flush 的操作。

#### 4. 更新总长度 (Update Total Length)

```go
length = b.recalLen(n)
return length, nil
```

- 调用 `recalLen(n)` 原子地增加 `UnsafeLinkBuffer` 的总长度字段。
- 返回最新的总长度。

## resetTail

```go
func (b *UnsafeLinkBuffer) resetTail(maxSize int) {
	if maxSize <= pagesize {
		return
	}
	b.write.next = newLinkBufferNode(0)
	b.write = b.write.next
	b.flush = b.write
}
```

它的主要作用是：**强制截断当前的写入节点，防止单个内存块（Node）无限膨胀，从而避免潜在的内存泄漏或 OOM（内存溢出）风险。**

这个操作的精髓在于**利用“只读节点”作为屏障，强制下一次写入触发新内存分配**。当 `resetTail` 执行后，`b.write` 指向了一个 `readonly` 的空节点。当下一次用户调用 `Malloc` 或 `WriteBinary` 时，会触发内部的 `growth`（扩容）逻辑：

## indexByte

```go
func (b *UnsafeLinkBuffer) indexByte(c byte, skip int) int {
	size := b.Len()
	if skip >= size {
		return -1
	}
	var unread, n, l int
	node := b.read
	for unread = size; unread > 0; unread -= n {
		l = node.Len()
		if l >= unread { // last node
			n = unread
		} else { // read full node
			n = l
		}

		if skip >= n {
			skip -= n
			node = node.next
			continue
		}
		i := bytes.IndexByte(node.Peek(n)[skip:], c)
		if i >= 0 {
			return (size - unread) + skip + i // past_read + skip_read + index
		}
		skip = 0 // no skip bytes
		node = node.next
	}
	return -1
}
```

**在底层的非连续链表结构中，查找特定字节 c 第一次出现的逻辑位置。** 同时，它支持 `skip` 参数，允许跳过开头的 skip 个字节开始查找。这是一个典型的**将物理非连续内存抽象为逻辑连续内存**的算法实现。

#### 1. 边界检查

```go
size := b.Len()
if skip >= size {
    return -1
}
```

- 首先获取缓冲区总的可读长度。
- 如果要求跳过的字节数 skip 超过或等于总长度，说明要找的位置根本不存在，直接返回 -1。

#### 2. 初始化游标

```go
var unread, n, l int
node := b.read
for unread = size; unread > 0; unread -= n {
    // ...
}
```

- unread: 剩余未检查的总字节数。初始为 size，每处理完一个节点就减去该节点处理的字节数 n。
- node: 从 read 节点开始遍历。

#### 3. 确定当前节点的搜索范围

```go
l = node.Len()
if l >= unread { // last node
    n = unread
} else { // read full node
    n = l
}
```

- 计算当前节点要检查的长度 n。
- 通常情况下 n 就是节点长度 l。
- 但在最后一个节点（或者 flush 指针所在的节点），有效数据可能只占节点的一部分，所以取 unread 和 l 的较小值。

#### 4. 高效跳过机制 (Skip Logic)

```go
// skip current node
if skip >= n {
    skip -= n
    node = node.next
    continue
}
```

这是性能优化的关键点：

- 如果剩余需要跳过的字节数 skip **大于等于** 当前节点的长度 n，说明这个节点完全不需要看。
- 直接减去 n，移动到下一个节点。
- **避免了无意义的内存扫描**，直接跨越整个内存块。

#### 5. 节点内搜索 (Search Logic)

```go
i := bytes.IndexByte(node.Peek(n)[skip:], c)
if i >= 0 {
    return (size - unread) + skip + i // past_read + skip_read + index
}
```

- node.Peek(n): 获取当前节点的切片。

- [skip:]: 利用切片操作，忽略掉当前节点头部需要跳过的字节。

- bytes.IndexByte: 调用 Go 标准库的汇编优化函数（通常使用 SIMD 指令）在切片中查找 c。

- **索引计算公式**:

  - size - unread: 之前**已经完整处理过**的节点总字节数。
  - skip: 在**当前节点**内部跳过的字节数。
  - i: bytes.IndexByte 返回的相对索引（相对于切片起始位置 skip 之后的偏移）。
  - 三者相加，即为 c 在整个 LinkBuffer 中的绝对逻辑偏移量。

#### 6. 重置 Skip 并继续

```go
skip = 0 // no skip bytes
node = node.next
```

- 如果代码执行到了这里，说明我们在当前节点进行了搜索（skip < n），但是没找到（i < 0）。
- 这意味着 skip 的要求已经被满足了（我们已经跳过了开头的那部分）。
- 对于**接下来的所有节点**，我们都应该从节点的第 0 个字节开始搜索，所以将 skip 重置为 0。