# Private Function



## recalLen

```go
func (b *UnsafeLinkBuffer) recalLen(delta int) (length int) {
    // 1. 处理缓存失效 (Cache Invalidation)
    // delta < 0 表示这是一次读取操作（数据长度减少）。
    // len(b.cachePeek) > 0 表示当前存在 Peek 缓存。
	if delta < 0 && len(b.cachePeek) > 0 {
        // 解释：
        // Peek 操作会把跨节点的数据拼接到 cachePeek 中。
        // 假设 Buffer 是 [Node1: "ABC"] -> [Node2: "DEF"]。
        // Peek(4) 后，cachePeek 变成 "ABCD"。
        // 如果此时调用 Next(1) 读取了 "A" (delta = -1)。
        // 此时 Buffer 逻辑上变成了 "BCDEF"。
        // 但 cachePeek 依然是 "ABCD"（包含已读的 "A"）。
        // 如果不清除 cachePeek，下次 Peek(3) 可能会直接返回 cachePeek[:3] 即 "ABC"，
        // 而用户期望的是 "BCD"。这就是脏读。
        
        // 因此，任何读取操作（导致 off 移动）都必须立即使当前的 Peek 缓存失效。
		b.cachePeek = b.cachePeek[:0]
	}
    // 2. 原子更新长度
    // 使用原子操作更新总长度。
    // 这支持了 SPSC (单生产者单消费者) 并发模型：
    // 一个 goroutine 在写 (delta > 0)，另一个在读 (delta < 0)。
    // 原子操作保证了 length 字段在并发访问下的准确性。
	return int(atomic.AddInt64(&b.length, int64(delta)))
}
```

这个方法用于更新 `LinkBuffer` 的总数据长度，并处理副作用（缓存失效）。

- **核心职责**：更新 length 字段。
- **关键副作用**：**维护数据一致性**。一旦发生数据消费（读操作），必须清除 Peek 缓存，防止下一次 Peek 读到过期的脏数据。
- **并发安全**：使用 atomic 操作，使得长度查询 (Len()) 和更新是线程安全的\。



## growth

```go
func (b *UnsafeLinkBuffer) growth(n int) {
	if n <= 0 {
		return
	}
	for b.write.getMode(readonlyMask) || cap(b.write.buf)-b.write.malloc < n {
		if b.write.next == nil {
			b.write.next = newLinkBufferNode(n)
			b.write = b.write.next
			return
		}
		b.write = b.write.next
	}
}
```

growth 函数是 `UnsafeLinkBuffer` 中负责**扩容**的核心逻辑。它的主要任务是确保 `b.write` 指针指向一个**可写的**、且**剩余空间足够**容纳 n 字节的节点。如果当前节点不满足条件，它会向后寻找可用节点，或者创建新节点。

- **情况 A：链表末尾 (b.write.next == nil)**
  - **动作**：当前已经是最后一个节点了，后面没有备用节点。
  - **处理**：调用 `newLinkBufferNode(n)` 创建一个新的节点，将其链接到链表末尾，并将 `b.write` 移动到这个新节点。
  - **返回**：既然已经创建了满足条件的新节点，扩容完成，直接 return。
- **情况 B：链表中间 (b.write.next != nil)**
  - **动作**：当前节点后面还有节点。
  - **场景**：这通常发生在缓冲区被 Reset 后，或者之前的 Flush 操作并没有完全使用完所有预分配的节点链。此时链表中存在“僵尸”节点或空闲节点。
  - **处理**：**复用**逻辑。直接将 b.write 指针向后移动一位 (b.write = b.write.next)。
  - **继续循环**：移动后，循环会再次执行，检查这个“下一个节点”是否满足条件（是否只读？空间够不够？）。如果够用，循环结束；不够用，继续往后找。



## isSingleNode

```go
func (b *UnsafeLinkBuffer) isSingleNode(readN int) (single bool) {
	if readN <= 0 {
		return true
	}
	l := b.read.Len()
	for l == 0 && b.read != b.flush {
		b.read = b.read.next
		l = b.read.Len()
	}
	return l >= readN
}
```

`isSingleNode` 是一个辅助方法，用于判断接下来的 `readN` 字节读取操作是否可以在**单个节点**内完成。如果是，就可以进行零拷贝读取；如果不是（跨节点），就需要分配内存进行拷贝。

- **为什么会有长度为 0 的节点？**
  - **完全读完**：一个节点的数据可能刚好被之前的 `Next` 操作全部读完了，`off `等于 `len(buf)`。
  - **零拷贝插入**：通过 `WriteDirect` 插入的节点，或者某些特殊操作可能产生长度为 0 的占位节点。
  - **Flush 边界**：`flush` 指针本身可能指向一个尚未填充数据的节点。
- **逻辑解释**：
  - 只要当前 `read` 节点的可读长度 l 为 0。
  - **且** `read` 指针还没有追上 `flush` 指针（`b.read != b.flush`）。这意味着后面肯定还有包含有效数据的节点。
  - **动作**：将 read 指针向后移动 (`b.read = b.read.next`)，跳过这个空节点，并重新计算新节点的长度 l。
- **目的**：这个循环确保了 b.read 最终指向的是**第一个包含实际数据的节点**（或者即使没数据了，也停在 flush 节点上）。这避免了后续逻辑在空节点上浪费时间，也保证了 l 代表的是下一个有效数据块的长度。



## memorySize

```go
func (b *LinkBuffer) memorySize() (bytes int) {
	for node := b.head; node != nil; node = node.next {
		bytes += cap(node.buf)
	}
	for _, c := range b.caches {
		bytes += cap(c)
	}
	bytes += cap(b.cachePeek)
	return bytes
}
```

`memorySize` 方法的作用是计算 `LinkBuffer` 当前**实际占用的物理内存总大小**（以字节为单位）。这个方法主要用于监控、统计或调试内存使用情况。它与 `Len()` 方法有本质区别：`Len()` 返回的是**逻辑上**可读的数据长度，而 `memorySize()` 返回的是**物理上**分配的内存总量。