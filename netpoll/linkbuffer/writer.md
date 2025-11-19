# Writerr

## Malloc

```go
func (b *UnsafeLinkBuffer) Malloc(n int) (buf []byte, err error) {
	if n <= 0 {
		return
	}
	b.mallocSize += n
	b.growth(n)
	return b.write.Malloc(n), nil
}
```

`Malloc(n int)` 的目标是向调用者提供一块大小为 n 字节的**空白内存切片**。调用者可以直接向这块切片中写入数据，而不需要先在外部创建一个切片写好数据，再调用 Write 拷贝进去。这省去了一次从“用户态缓冲区”到“库内部缓冲区”的内存拷贝。

#### A. 状态记账 (b.mallocSize += n)

这里只更新了 `mallocSize`，而**没有更新 b.length**。

- `length` 代表**可读**数据的长度。
- `mallocSize` 代表**已分配但未提交**的数据长度。
  这体现了 `LinkBuffer` 的“两阶段写入”机制：先 `Malloc`（占坑），再 `Flush`（发布）。在调用 `Flush` 之前，这 n 个字节对读取者（Reader）是不可见的。

#### B. 扩容机制 (b.growth(n))

这是保证 `Malloc` 成功的关键。`growth` 方法（在文件后面定义）会检查 `b.write` 指向的当前尾节点：

1. **检查剩余空间**: 当前节点的剩余容量 (cap - malloc) 是否足够容纳 n 字节？
2. **检查只读状态**: 当前节点是否是只读的（例如通过 Slice 产生的引用节点）？

如果空间不足或节点只读，`growth` 会创建一个新的 `linkBufferNode`，将其链接到链表末尾，并将 `b.write` 指针移动到这个新节点。
*注意：如果 n 很大（超过默认的 4KB），新创建的节点容量至少会是 n，确保一次 Malloc 总是能返回**连续**的内存。*

#### C. 节点切分 (b.write.Malloc(n))

当 `growth` 保证了 `b.write` 节点有足够空间后，调用节点自身的 `Malloc` 方法。
这个方法非常简单：它基于节点内部的 `buf` 数组，根据当前的 `malloc` 偏移量，切出一个长度为 n 的切片返回，并更新节点的 `malloc` 偏移量。

## MallocAck

```go
func (b *UnsafeLinkBuffer) MallocAck(n int) (err error) {
    // 1. 参数校验
	if n < 0 {
		return fmt.Errorf("link buffer malloc ack[%d] invalid", n)
	}
    // 2. 重置全局状态
	b.mallocSize = n
	b.write = b.flush // 更新总的预分配大小为 n
					// 将 write 指针“回滚”到 flush 指针的位置
                      // flush 指向的是上一次提交后的末尾，也就是本次 Malloc 开始的地方
                      // 我们将从这里开始重新计算 n 个字节在哪里结束

	var l int
    // 3. 寻找第 n 个字节的落脚点
	for ack := n; ack > 0; ack = ack - l {
        // 计算当前节点中“新 Malloc 出来的数据”长度
        // len(b.write.buf) 是该节点之前已 Flush 的数据长度
        // b.write.malloc 是该节点当前分配到的总长度
		l = b.write.malloc - len(b.write.buf)
		if l >= ack {
            // 情况 A：剩余需要的 ack 字节完全落在当前节点内
            // 将当前节点的 malloc 指针缩减到正确的位置
			b.write.malloc = ack + len(b.write.buf)
			break
		}
        // 情况 B：当前节点的新数据全部都要保留，但还不够 ack 的数量
        // write 指针移动到下一个节点，继续找
		b.write = b.write.next
	}
    // 4. 清理后续节点（丢弃多余部分）
    // 从新的 write 节点的下一个节点开始遍历
	for node := b.write.next; node != nil; node = node.next {
        // 重置节点的 malloc 指针等于 off（即该节点变为空，没有新数据）
        // 缩减 slice 长度，丢弃数据
		node.malloc, node.refer, node.buf = node.off, 1, node.buf[:node.off]
	}
	return nil
}
```

**保留前** **n** **个已 Malloc 的字节，丢弃（回滚）剩余的部分。**

## Flush

```go
func (b *UnsafeLinkBuffer) Flush() (err error) {
	b.mallocSize = 0

	if cap(b.write.buf) > pagesize {
		b.write.next = newLinkBufferNode(0)
		b.write = b.write.next
	}
	var n int
	for node := b.flush; node != b.write.next; node = node.next {
		delta := node.malloc - len(node.buf)
		if delta > 0 {
			n += delta
			node.buf = node.buf[:node.malloc]
		}
	}
	b.flush = b.write

	b.recalLen(n)
	return nil
}
```


`Flush` 方法是 `UnsafeLinkBuffer` 写入生命周期的终点。它的核心作用是**提交数据**，将之前通过 Malloc 预分配并填充的“不可见”数据，正式转换为“可读”数据。

### 核心逻辑概览

1. **重置计数器**: 清零 `mallocSize`。
2. **大节点优化**: 检查并处理过大的尾部节点。
3. **批量提交**: 遍历从 `flush` 到 `write` 的所有节点，更新它们的 `buf` 长度。
4. **同步指针**: 让 `flush` 指针追上 `write` 指针。
5. **更新总长**: 原子更新整个 `Buffer` 的 `length`，使数据对外可见。

#### 1. 重置预分配计数

```go
b.mallocSize = 0
```

`mallocSize` 只是一个临时的记账变量，用于追踪“当前这批次”写入了多少。一旦提交，这部分大小就会加到总的 `length` 中，所以这里归零。

#### 2. 尾部节点大小检查 (`FIXME` 部分)

```go
// FIXME: The tail node must not be larger than 8KB to prevent Out Of Memory.
if cap(b.write.buf) > pagesize {
    b.write.next = newLinkBufferNode(0)
    b.write = b.write.next
}
```

- **目的**: 防止内存滞留（Memory Hoarding）和优化内存回收。
- **逻辑**: 如果当前的写入节点（write）容量非常大（超过 `pagesize`，通常是 `4KB` 或 `8KB`），代码决定**不再继续使用这个节点进行后续的写入**。
- **操作**: 它强制创建一个新的、标准大小的节点链接到末尾，并将 write 指针移过去。
- **原因**:
  - 如果一个巨大的节点（比如 10MB）只被填充了 5MB，如果不切断，下次 Malloc 还会继续在这 10MB 上追加。这会导致这个巨大的内存块长期无法被 Release。
  - 通过切断，当前的巨大节点被填满（或部分填满）并提交后，一旦被读取完毕，就可以立即被回收。新的写入将发生在新的小节点上。

#### 3. 提交数据的循环 (核心)

```go
var n int
for node := b.flush; node != b.write.next; node = node.next {
    delta := node.malloc - len(node.buf)
    if delta > 0 {
        n += delta
        node.buf = node.buf[:node.malloc]
    }
}
```

- **遍历范围**: 从 `b.flush` 开始，一直处理到 `b.write` 节点（包含 `b.write`）。
  - *注意*: 这里的 `b.write.next` 可能是 `nil`，也可能是上面代码块刚创建的新空节点。无论哪种情况，循环都会正确覆盖所有包含新数据的节点。
- **计算增量 (delta)**:
  - `node.malloc` 是新数据的终点。
  - `len(node.buf)` 是旧数据的终点。
  - `delta` 就是本次 `Flush` 在该节点上新增的数据量。
- **更新视图 (`node.buf = node.buf[:node.malloc]`)**:
  - 这是最关键的一行。它通过 **Reslicing（重新切片）** 扩展了 buf 的长度。
  - 在这一行执行之前，buf 看不到新写入的数据；执行之后，buf 的长度变大了，新数据正式变为“可读”。

#### 4. 指针与长度更新

```go
b.flush = b.write
// re-cal length
b.recalLen(n)
```

- **同步指针**: `flush` 指针此前一直停留在上一次提交的位置。现在数据都提交了，它必须移动到最新的 `write` 位置，准备迎接下一次 `Malloc`。

## WriteBuffer

```go
// WriteBuffer will not submit(e.g. Flush) data to ensure normal use of MallocLen.
// you must actively submit before read the data.
// The argument buf can't be used after calling WriteBuffer. (set it to nil)
func (b *UnsafeLinkBuffer) WriteBuffer(buf *LinkBuffer) (err error) {
	if buf == nil {
		return
	}
	bufLen, bufMallocLen := buf.Len(), buf.MallocLen()
	if bufLen+bufMallocLen <= 0 {
		return nil
	}
	b.write.next = buf.read
	b.write = buf.write

	// close buf, prevents reuse.
	for buf.head != buf.read {
		nd := buf.head
		buf.head = buf.head.next
		nd.Release()
	}
	for buf.write = buf.write.next; buf.write != nil; {
		nd := buf.write
		buf.write = buf.write.next
		nd.Release()
	}
	buf.length, buf.mallocSize, buf.head, buf.read, buf.flush, buf.write = 0, 0, nil, nil, nil, nil

	// DON'T MODIFY THE CODE BELOW UNLESS YOU KNOW WHAT YOU ARE DOING !
	//
	// You may encounter a chain of bugs and not be able to
	// find out within a week that they are caused by modifications here.
	//
	// After release buf, continue to adjust b.
	b.write.next = nil
	if bufLen > 0 {
		b.recalLen(bufLen)
	}
	b.mallocSize += bufMallocLen
	return nil
}
```

`WriteBuffer` 是 `UnsafeLinkBuffer` 中一个非常高效但也非常“危险”的方法。它的核心功能是**将另一个 `LinkBuffer (buf)` 的所有数据“嫁接”到当前 `LinkBuffer (b)` 的末尾**。*O(1)* 复杂度的零拷贝操作，因为它操作的是链表指针，而不是复制内存中的字节。

### 核心特性

1. **零拷贝拼接 (Zero-Copy Splicing)**: 直接修改链表指针，将 buf 的有效节点链挂到 b 的后面。
2. **所有权转移 (Ownership Transfer)**: b 接管 buf 的数据，buf 在操作后被彻底清空并销毁（不可再用）。
3. **状态保留**:
   - buf 中原本**可读**的数据，在 b 中立即变为**可读**。
   - buf 中原本**已 Malloc 但未 Flush** 的数据，在 b 中依然保持 **已 Malloc** 状态（需要调用者后续对 b 执行 Flush 才能可见）。

#### 1. 指针嫁接 (The Graft)

```go
b.write.next = buf.read
b.write = buf.write
```

这是最神奇的两行代码：

- `b.write.next = buf.read`: 将 b 的尾部（写指针）指向 `buf` 的头部（读指针）。两条链表连在了一起。
- `b.write = buf.write`: 更新 b 的写指针，使其指向原来 `buf `的写指针。
- **结果**: `b` 现在的样子是 [`b`的原数据] -> [`buf`的数据]。

#### 2. 销毁源 Buffer (Destroy Source)

由于 buf 的有效节点已经被 b 拿走了，我们需要清理 buf 中那些“没用”的节点，防止内存泄漏。

- **清理头部废弃节点**:

  ```go
  for buf.head != buf.read { ... nd.Release() ... }
  ```

  `buf` 中 `read` 之前的节点是已经被消费过的废弃节点，b 不需要它们，所以必须释放回对象池。

- **清理尾部空闲节点**:

  ```go
  for buf.write = buf.write.next; buf.write != nil; { ... nd.Release() ... }
  ```

  `buf` 的 `write` 指针之后可能挂着一些预分配但完全没用到的空节点。既然 `b` 已经接管了 `write` 指针，那么原本 `buf.write` 后面的节点就成了孤儿，也需要释放。

- **彻底抹除源对象**:

  ```go
  buf.length, ... = 0, 0, nil, ...
  ```

  将 `buf` 的所有字段置空，防止调用者误用。

#### 3. 调整目标 Buffer 状态 (Adjust Destination)

这部分代码被标记了严重的警告注释（DON'T MODIFY...），因为它涉及精密的状态同步。

```go
b.write.next = nil
```

确保新的尾部节点的 `next` 是 `nil`，切断任何可能的悬挂指针。

```go
if bufLen > 0 {
    b.recalLen(bufLen)
}
```

- `buf` 中原本就是 **Readable** (已 Flush) 的数据长度 (`bufLen`)，直接加到 b 的 length 上。这意味着这部分数据在 b 中**立即可读**。

```go
b.mallocSize += bufMallocLen
```

- `buf` 中原本 **Malloc'd** (未 Flush) 的数据长度 (`bufMallocLen`)，加到 b 的 `mallocSize` 上。
- **关键点**: 这部分数据在 b 中**仍然不可读**。`LinkBuffer` 严格遵守了状态的一致性。调用者如果想读这部分数据，必须在调用 `WriteBuffer `之后，再对 b 调用 Flush()。

## WriteBinary

```go
// WriteBinary implements Writer.
func (b *UnsafeLinkBuffer) WriteBinary(p []byte) (n int, err error) {
	n = len(p)
	if n == 0 {
		return
	}
	b.mallocSize += n

	// TODO: Verify that all nocopy is possible under mcache.
	if n > BinaryInplaceThreshold {
		// expand buffer directly with nocopy
		b.write.next = newLinkBufferNode(0)
		b.write = b.write.next
		b.write.buf, b.write.malloc = p[:0], n
		return n, nil
	}
	// here will copy
	b.growth(n)
	buf := b.write.Malloc(n)
	return copy(buf, p), nil
}
```

`WriteBinary` 是 `UnsafeLinkBuffer` 中实现 Writer 接口的核心方法。它的设计非常精妙，采用了一种**混合写入策略（Hybrid Write Strategy）**，在“内存拷贝开销”和“内存管理开销”之间寻找最佳平衡点。

### 路径 A：大数据零拷贝 ( > 4KB )

当写入的数据量较大时，拷贝操作会消耗大量 CPU 时间和内存带宽。为了优化性能，`LinkBuffer` 选择**直接引用**用户传入的切片。

```go
// expand buffer directly with nocopy
b.write.next = newLinkBufferNode(0)
b.write = b.write.next
b.write.buf, b.write.malloc = p[:0], n
return n, nil
```

1. **创建特殊节点**: `newLinkBufferNode(0)` 创建一个新的链表节点。传入 0 会将该节点标记为 **ReadOnly**（非池化管理）。这意味着这个节点的底层内存不是由 `LinkBuffer` 的对象池管理的，而是外部传入的。
2. **挂载节点**: 将新节点挂到链表末尾 (b.write.next)，并移动 write 指针。
3. **引用内存 (关键)**:
   - `b.write.buf` = p[:0]: 让节点的内部切片**直接指向**用户传入的 p 的底层数组。使用 [:0] 是为了重置长度，但保留容量和指针。
   - b.write.malloc = n: 设置写入偏移量为数据长度。
4. **效果**: 没有发生任何内存拷贝。LinkBuffer 直接持有了用户数据的引用。
5. **风险**: 这是一种“不安全”的操作。因为 `LinkBuffer` 和调用者共享了同一块内存。如果调用者在调用 `WriteBinary` 后修改了 p 中的内容，`LinkBuffer` 中的数据也会随之改变（可能导致数据竞争或逻辑错误）。

### 路径 B：小数据拷贝 ( <= 4KB )

当数据量较小时，拷贝的开销很低，而创建新节点的开销相对较高（且会导致内存碎片）。因此，直接拷贝到现有的缓冲区是更优的选择。

```go
// here will copy
b.growth(n)
buf := b.write.Malloc(n)
return copy(buf, p), nil
```

1. **扩容检查 (growth)**: 检查当前 write 节点是否有足够的剩余空间。如果没有，分配一个新的标准大小（如 4KB）的节点。
2. **分配空间 (Malloc)**: 在当前节点中划出一块大小为 n 的区域。
3. **数据拷贝 (copy)**: 将用户数据 p 复制到 LinkBuffer 的内部内存中。
4. **效果**: 数据被安全地复制了一份。后续用户对 p 的修改不会影响 `LinkBuffer`。

## WriteDirect

```go
func (b *UnsafeLinkBuffer) WriteDirect(extra []byte, remainLen int) error {
	n := len(extra)
	if n == 0 || remainLen < 0 {
		return nil
	}
	// find origin
	origin := b.flush
	malloc := b.mallocSize - remainLen // calculate the remaining malloc length
	for t := origin.malloc - len(origin.buf); t < malloc; t = origin.malloc - len(origin.buf) {
		malloc -= t
		origin = origin.next
	}
	// Add the buf length of the original node
	// `malloc` is the origin buffer offset that already malloced, the extra buffer should be inserted after that offset.
	malloc += len(origin.buf)

	// Create dataNode and newNode and insert them into the chain
	// dataNode wrap the user buffer extra, and newNode wrap the origin left netpoll buffer
	// - originNode{buf=origin, off=0, malloc=malloc, readonly=true} : non-reusable
	// - dataNode{buf=extra, off=0, malloc=len(extra), readonly=true} : non-reusable
	// - newNode{buf=origin, off=malloc, malloc=origin.malloc, readonly=false} : reusable
	dataNode := newLinkBufferNode(0) // zero node will be set by readonly mode
	dataNode.buf, dataNode.malloc = extra[:0], n

	if remainLen > 0 {
		// split a single buffer node to originNode and newNode
		newNode := newLinkBufferNode(0)
		newNode.off = malloc
		newNode.buf = origin.buf[:malloc]
		newNode.malloc = origin.malloc
		newNode.setMode(readonlyMask, false)
		origin.malloc = malloc
		origin.setMode(readonlyMask, true)

		// link nodes
		dataNode.next = newNode
		newNode.next = origin.next
		origin.next = dataNode
	} else {
		// link nodes
		dataNode.next = origin.next
		origin.next = dataNode
	}

	// adjust b.write
	for b.write.next != nil {
		b.write = b.write.next
	}

	b.mallocSize += n
	return nil
}
```

通常的 `Write` 操作都是在缓冲区末尾追加数据，而 `WriteDirect` 允许你在**已分配但未提交（Malloc'd）**的数据流中间插入一段外部数据。

###  1.核心目标与场景

- **目标**: 在不移动现有数据、不拷贝新数据的前提下，将 `extra` 缓冲区插入到距离当前 `write` 指针末尾 `remainLen` 的位置。
- **典型场景**: **协议头插入**。
  1. 你先 `Malloc` 了一块很大的空间用于存放 `Response Body`。
  2. `Body` 写完后，你需要在这个 `Body` 之前（但在之前的某些数据之后）插入一个协议头（Header）。
  3. 这个 `Header` 是现成的 `[]byte`。
  4. 使用 `WriteDirect` 可以直接把 `Header` “插”进去，而不需要把 `Body` 向后挪动。

### 2. 参数含义

- `extra []byte`: 要插入的外部数据片段。
- `remainLen int`: **保留长度**。表示在插入点之后，还有多少字节的 `Malloc` 空间是需要保留在后面的。
  - 插入点位置 = 当前总`MallocSize - remainLen`

### 3. 核心逻辑：节点分裂 (Node Splitting)

为了实现“中间插入”且不拷贝数据，LinkBuffer 必须对链表节点进行手术：将一个物理节点**逻辑上分裂**成两个，中间塞入新节点。

假设当前的内存布局是这样的（`origin` 节点）：

```
[ ... 前序数据 (Part 1) ... | ... 后续数据 (Part 2) ... ]
^ start                     ^ split point               ^ end
```

我们需要把它变成：

```
[ Part 1 ] -> [ extra ] -> [ Part 2 ]
```

#### 代码流程解析：

**第一步：定位分裂点 (origin)**

```go
// 计算插入点的绝对偏移量
malloc := b.mallocSize - remainLen 
// 遍历链表找到包含这个偏移量的节点 (origin)
for ... { ... origin = origin.next }
// 计算在 origin 节点内部的相对偏移量
malloc += len(origin.buf)
```

这段代码找到了需要动手术的那个节点 `origin`，以及手术刀下刀的位置 `malloc`。

**第二步：创建中间节点 (dataNode)**

```go
dataNode := newLinkBufferNode(0)
dataNode.buf, dataNode.malloc = extra[:0], n
```

创建一个只读节点，直接引用用户传入的 `extra` 数据。

**第三步：执行分裂 (最关键的部分)**

如果 `remainLen > 0`，说明插入点在节点中间，需要分裂：

1. **创建右半部分 (newNode)**:

   ```go
   newNode := newLinkBufferNode(0)
   newNode.off = malloc                // 起始位置是分裂点
   newNode.buf = origin.buf[:malloc]   // 共享底层的 slice header (注意这里主要是为了复用cap)
   newNode.malloc = origin.malloc      // 结束位置是原节点的结束位置
   newNode.setMode(readonlyMask, false)// 标记为可回收（reusable）
   ```

   newNode 代表了原节点的后半部分。**注意：newNode 和 origin 共享同一个底层数组！**

2. **修改左半部分 (origin)**:

   ```go
   origin.malloc = malloc             // 结束位置截断到分裂点
   origin.setMode(readonlyMask, true) // 标记为只读！
   ```

   `origin` 被修改为只代表前半部分。**关键点**：将 `origin` 标记为只读是为了**防止双重释放**。因为 `origin` 和 `newNode` 共享内存，必须只有一个节点负责在 `Release` 时回收底层内存（这里交给了 `newNode`）。

3. **链表重组**:

   ```go
   dataNode.next = newNode
   newNode.next = origin.next
   origin.next = dataNode
   ```

   原来的 `origin -> next` 变成了 `origin -> dataNode -> newNode -> next`。

**第四步：调整尾部指针**
由于链表结构变了，`b.write` 指针可能需要更新，代码遍历到新的末尾。

### 4. 图解变化

**Before:**

```
Node A (origin)
[ 11111 | 22222 ]
        ^ split
```

**After:**

```
Node A (origin)   ->   Node B (dataNode)   ->   Node C (newNode)
[ 11111 ]              [ extra data ]           [ 22222 ]
(Shared Mem)           (User Mem)               (Shared Mem)
(ReadOnly)             (ReadOnly)               (Writable/Owner)
```

### 5. 为什么“不可混用”？

注释警告 `WriteDirect cannot be mixed with WriteString or WriteBinary`，原因如下：

1. **状态复杂性**: `WriteDirect` 依赖于对 `mallocSize` 和节点结构的精确计算。如果混合使用普通写入，`remainLen` 的计算会变得极其困难，容易出错。
2. **内存布局假设**: 它假设这种“插入”操作是构建数据包的特定阶段。
3. **节点属性修改**: 它修改了现有节点的 `readonly` 属性和 malloc 边界。如果在不恰当的时机调用，可能会破坏 `LinkBuffer` 的内部一致性。

## Close

```go
func (b *UnsafeLinkBuffer) Close() (err error) {
	atomic.StoreInt64(&b.length, 0)
	b.mallocSize = 0
	// just release all
	b.Release()
	for node := b.head; node != nil; {
		nd := node
		node = node.next
		nd.Release()
	}
	b.head, b.read, b.flush, b.write = nil, nil, nil, nil
	return nil
}
```

`Close` 方法是 `UnsafeLinkBuffer` 生命周期的终结者。它的主要职责是**清理战场**：彻底释放所有持有的资源，并将对象重置为初始（不可用）状态，防止内存泄漏。