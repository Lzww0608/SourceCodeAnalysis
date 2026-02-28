# History模块Queue实现数据结构详解

## 目录

1. [Queue接口实现概览](#1-queue接口实现概览)
2. [核心队列实现](#2-核心队列实现)
3. [辅助数据结构](#3-辅助数据结构)
4. [架构设计分析](#4-架构设计分析)

---

## 1. Queue接口实现概览

### 1.1 实现类层次结构

```
Queue (interface)
  │
  ├── immediateQueue          // 立即队列
  │     └── queueBase         // 继承队列基类
  │
  ├── scheduledQueue          // 定时队列
  │     └── queueBase         // 继承队列基类
  │
  ├── memoryScheduledQueue    // 内存定时队列（独立实现）
  │
  └── SpeculativeWorkflowTaskTimeoutQueue  // 推测性超时队列
        └── memoryScheduledQueue           // 组合内存队列
```

### 1.2 实现分类

| 实现类 | 文件路径 | 用途 | 任务类型 |
|--------|---------|------|---------|
| **immediateQueue** | queue_immediate.go | 处理立即执行的任务 | Transfer, Replication, Visibility, Outbound |
| **scheduledQueue** | queue_scheduled.go | 处理定时执行的任务 | Timer, Archival |
| **memoryScheduledQueue** | memory_scheduled_queue.go | 内存中的定时任务 | MemoryTimer |
| **SpeculativeWorkflowTaskTimeoutQueue** | speculative_workflow_task_timeout_queue.go | WorkflowTask推测性超时 | WorkflowTaskTimeout |

---

## 2. 核心队列实现

### 2.1 queueBase（队列基类）

**文件**: [service/history/queues/queue_base.go](file:///Users/bytedance/code/temporal/service/history/queue_base.go)

#### 2.1.1 数据结构

```go
type queueBase struct {
    shard hshard.Context
    
    // 状态管理
    status     int32
    shutdownCh chan struct{}
    shutdownWG sync.WaitGroup
    
    // 队列配置
    category       tasks.Category
    options        *Options
    scheduler      Scheduler
    rescheduler    Rescheduler
    timeSource     clock.TimeSource
    monitor        *monitorImpl
    mitigator      *mitigatorImpl
    grouper        Grouper
    logger         log.Logger
    metricsHandler metrics.Handler
    
    // 任务加载
    paginationFnProvider PaginationFnProvider
    executableFactory    ExecutableFactory
    
    // 状态跟踪
    lastRangeID                    int64
    exclusiveDeletionHighWatermark tasks.Key
    nonReadableScope               Scope
    readerRateLimiter              quotas.RequestRateLimiter
    readerGroup                    *ReaderGroup
    nextForceNewSliceTime          time.Time
    
    // 检查点机制
    checkpointRetrier backoff.Retrier
    checkpointTimer   *time.Timer
    
    alertCh <-chan *Alert
}
```

#### 2.1.2 核心职责

**1. 任务生命周期管理**
- 从数据库加载任务
- 提交任务到执行器
- 跟踪任务执行状态
- 持久化处理进度

**2. Reader管理**
- 管理多个Reader实例
- 协调Reader之间的工作
- 处理Reader的创建和销毁

**3. 检查点机制**
```go
func (p *queueBase) checkpoint() {
    // 1. 收缩所有Reader的Slice
    var tasksCompleted int
    p.readerGroup.ForEach(func(_ int64, r Reader) {
        tasksCompleted += r.ShrinkSlices()
    })
    
    // 2. 执行slicePredicateAction
    // 将非通用谓词的Slice移动到非默认Reader
    
    // 3. 持久化队列状态
    p.shard.UpdateQueueState(
        p.category,
        ToPersistenceQueueState(queueState{
            readerScopes:                 p.readerGroup.ReaderScopes(),
            exclusiveReaderHighWatermark: p.exclusiveReaderHighWatermark,
        }),
    )
}
```

**4. 故障转移支持**
```go
func (p *queueBase) FailoverNamespace(namespaceID string) {
    // 重新调度指定命名空间的所有任务
    p.rescheduler.Reschedule(namespaceID)
}
```

#### 2.1.3 关键设计

**Range管理**：
```go
func (p *queueBase) processNewRange() {
    // 获取新的读取范围
    newMaxKey := p.shard.GetQueueExclusiveHighReadWatermark(p.category)
    
    // 分割不可读范围
    slices := make([]Slice, 0, 1)
    if p.nonReadableScope.CanSplitByRange(newMaxKey) {
        var newReadScope Scope
        newReadScope, p.nonReadableScope = p.nonReadableScope.SplitByRange(newMaxKey)
        
        // 创建新的Slice
        slices = append(slices, NewSlice(
            p.paginationFnProvider,
            p.executableFactory,
            p.monitor,
            newReadScope,
            p.grouper,
            p.options.ReaderOptions.MaxPredicateSize,
        ))
    }
    
    // 将Slice添加到默认Reader
    reader, ok := p.readerGroup.ReaderByID(DefaultReaderId)
    if !ok {
        p.readerGroup.NewReader(DefaultReaderId, slices...)
        return
    }
    
    // 定期强制创建新Slice，避免单个Slice无限增长
    if now := p.timeSource.Now(); now.After(p.nextForceNewSliceTime) {
        reader.AppendSlices(slices...)
        p.nextForceNewSliceTime = now.Add(forceNewSliceDuration)
    } else {
        reader.MergeSlices(slices...)
    }
}
```

---

### 2.2 immediateQueue（立即队列）

**文件**: [service/history/queues/queue_immediate.go](file:///Users/bytedance/code/temporal/service/history/queues/queue_immediate.go)

#### 2.2.1 数据结构

```go
type immediateQueue struct {
    *queueBase  // 继承队列基类
    
    // 通知机制
    notifyCh chan struct{}
}
```

#### 2.2.2 核心特性

**立即执行**：
- 任务立即可执行，无需等待
- 通过channel通知机制触发处理
- 无需定时器管理

#### 2.2.3 工作流程

```go
func (p *immediateQueue) Start() {
    // 1. CAS确保只启动一次
    if !atomic.CompareAndSwapInt32(
        &p.status, 
        common.DaemonStatusInitialized, 
        common.DaemonStatusStarted,
    ) {
        return
    }
    
    // 2. 启动基类
    p.queueBase.Start()
    
    // 3. 启动事件处理循环
    p.shutdownWG.Add(1)
    go p.processEventLoop()
    
    // 4. 发送初始通知
    p.notify()
}

func (p *immediateQueue) processEventLoop() {
    defer p.shutdownWG.Done()
    
    for {
        select {
        case <-p.notifyCh:
            // 有新任务，立即处理
            p.processNewRange()
            
        case <-p.shutdownCh:
            return
        }
    }
}

func (p *immediateQueue) NotifyNewTasks(tasks []tasks.Task) {
    if len(tasks) == 0 {
        return
    }
    
    // 发送通知信号
    p.notify()
}

func (p *immediateQueue) notify() {
    select {
    case p.notifyCh <- struct{}{}:
        // 通知成功
    default:
        // 已有待处理通知，无需重复
    }
}
```

#### 2.2.4 使用场景

| 任务类型 | 说明 | 示例 |
|---------|------|------|
| **Transfer** | 转移任务 | 向Matching Service发送WorkflowTask/ActivityTask |
| **Replication** | 复制任务 | 跨集群状态复制 |
| **Visibility** | 可见性任务 | 更新搜索索引 |
| **Outbound** | 出站任务 | 发送回调、Nexus操作 |

---

### 2.3 scheduledQueue（定时队列）

**文件**: [service/history/queues/queue_scheduled.go](file:///Users/bytedance/code/temporal/service/history/queues/queue_scheduled.go)

#### 2.3.1 数据结构

```go
type scheduledQueue struct {
    *queueBase  // 继承队列基类
    
    // 定时器管理
    timerGate   timer.Gate
    newTimerCh  chan struct{}
    newTimeLock sync.Mutex
    newTime     time.Time
    
    // 前瞻机制
    lookAheadCh               chan struct{}
    lookAheadRateLimitRequest quotas.Request
}
```

#### 2.3.2 核心特性

**延迟执行**：
- 任务有可见时间（VisibilityTime）
- 需要等待到指定时间才能执行
- 使用TimerGate管理定时器

**前瞻机制**：
- 提前加载即将到期的任务
- 减少任务到期时的延迟
- 提高处理效率

#### 2.3.3 工作流程

```go
func (p *scheduledQueue) Start() {
    if !atomic.CompareAndSwapInt32(
        &p.status, 
        common.DaemonStatusInitialized, 
        common.DaemonStatusStarted,
    ) {
        return
    }
    
    p.queueBase.Start()
    
    p.shutdownWG.Add(1)
    go p.processEventLoop()
    
    // 启动时设置初始时间为time.Time{}（立即开始）
    p.notify(time.Time{})
}

func (p *scheduledQueue) processEventLoop() {
    defer p.shutdownWG.Done()
    
    for {
        select {
        case <-p.newTimerCh:
            // 有新的定时器设置
            p.processNewRange()
            
        case <-p.timerGate.FireChan():
            // 定时器到期
            p.processNewRange()
            
        case <-p.lookAheadCh:
            // 前瞻加载
            p.processNewRange()
            
        case <-p.shutdownCh:
            return
        }
    }
}

func (p *scheduledQueue) NotifyNewTasks(tasks []tasks.Task) {
    if len(tasks) == 0 {
        return
    }
    
    // 找到最早的可见时间
    newTime := tasks[0].GetVisibilityTime()
    for _, task := range tasks {
        ts := task.GetVisibilityTime()
        if ts.Before(newTime) {
            newTime = ts
        }
    }
    
    // 更新定时器
    p.notify(newTime)
}

func (p *scheduledQueue) notify(newTime time.Time) {
    p.newTimeLock.Lock()
    defer p.newTimeLock.Unlock()
    
    // 如果新时间更早，更新定时器
    if newTime.Before(p.newTime) || p.newTime.IsZero() {
        p.newTime = newTime
        p.timerGate.Update(newTime)
        
        select {
        case p.newTimerCh <- struct{}{}:
        default:
        }
    }
}
```

#### 2.3.4 数据库查询优化

```go
func NewScheduledQueue(...) *scheduledQueue {
    paginationFnProvider := func(r Range) collection.PaginationFn[tasks.Task] {
        return func(paginationToken []byte) ([]tasks.Task, []byte, error) {
            ctx, cancel := newQueueIOContext()
            defer cancel()
            
            request := &persistence.GetHistoryTasksRequest{
                ShardID:             shard.GetShardID(),
                TaskCategory:        category,
                // 关键：按可见时间查询
                InclusiveMinTaskKey: tasks.NewKey(r.InclusiveMin.FireTime, 0),
                ExclusiveMaxTaskKey: tasks.NewKey(
                    r.ExclusiveMax.FireTime.Add(persistence.ScheduledTaskMinPrecision),
                    0,
                ),
                BatchSize:     options.BatchSize(),
                NextPageToken: paginationToken,
            }
            
            resp, err := shard.GetExecutionManager().GetHistoryTasks(ctx, request)
            if err != nil {
                return nil, nil, err
            }
            
            // 过滤超出范围的任务
            for len(resp.Tasks) > 0 && !r.ContainsKey(resp.Tasks[0].GetKey()) {
                resp.Tasks = resp.Tasks[1:]
            }
            
            for len(resp.Tasks) > 0 && !r.ContainsKey(resp.Tasks[len(resp.Tasks)-1].GetKey()) {
                resp.Tasks = resp.Tasks[:len(resp.Tasks)-1]
                resp.NextPageToken = nil
            }
            
            return resp.Tasks, resp.NextPageToken, nil
        }
    }
    
    // ... 其他初始化
}
```

#### 2.3.5 使用场景

| 任务类型 | 说明 | 可见时间 |
|---------|------|---------|
| **Timer** | 定时器任务 | workflow.sleep()设置的时间 |
| **Archival** | 归档任务 | Workflow完成后延迟归档 |

---

### 2.4 memoryScheduledQueue（内存定时队列）

**文件**: [service/history/queues/memory_scheduled_queue.go](file:///Users/bytedance/code/temporal/service/history/queues/memory_scheduled_queue.go)

#### 2.4.1 数据结构

```go
type memoryScheduledQueue struct {
    scheduler ctasks.Scheduler[ctasks.Task]
    
    // 任务队列（优先队列，按可见时间排序）
    taskQueue     collection.Queue[Executable]
    nextTaskTimer *time.Timer
    newTaskCh     chan Executable
    
    timeSource     clock.TimeSource
    logger         log.Logger
    metricsHandler metrics.Handler
    
    status     int32
    shutdownCh chan struct{}
    shutdownWG sync.WaitGroup
}
```

#### 2.4.2 核心特性

**纯内存实现**：
- 不依赖数据库持久化
- 任务存储在内存优先队列中
- 适用于临时性、短期任务

**优先队列**：
```go
func executableVisibilityTimeCompareLess(
    this Executable,
    that Executable,
) bool {
    // 按可见时间排序，最早的任务优先
    return this.GetVisibilityTime().Before(that.GetVisibilityTime())
}
```

#### 2.4.3 工作流程

```go
func (q *memoryScheduledQueue) Start() {
    if !atomic.CompareAndSwapInt32(&q.status, common.DaemonStatusInitialized, common.DaemonStatusStarted) {
        return
    }
    
    q.shutdownWG.Add(1)
    go q.processQueueLoop()
}

func (q *memoryScheduledQueue) processQueueLoop() {
    defer q.shutdownWG.Done()
    
    for {
        select {
        case <-q.shutdownCh:
            return
        default:
        }
        
        select {
        case <-q.shutdownCh:
            return
            
        case newTask := <-q.newTaskCh:
            // 新任务到达
            var nextTaskTime time.Time
            if !q.taskQueue.IsEmpty() {
                nextTaskTime = q.taskQueue.Peek().GetVisibilityTime()
            }
            
            // 添加到优先队列
            q.taskQueue.Add(newTask)
            
            // 如果新任务更早，更新定时器
            if nextTaskTime.IsZero() || newTask.GetVisibilityTime().Before(nextTaskTime) {
                q.updateTimer()
            }
            
        case <-q.nextTaskTimer.C:
            // 定时器到期，处理任务
            q.processReadyTasks()
        }
    }
}

func (q *memoryScheduledQueue) Add(task Executable) {
    q.newTaskCh <- task
}

func (q *memoryScheduledQueue) updateTimer() {
    if q.taskQueue.IsEmpty() {
        q.nextTaskTimer.Stop()
        return
    }
    
    // 获取最早任务的可见时间
    nextTaskTime := q.taskQueue.Peek().GetVisibilityTime()
    now := q.timeSource.Now()
    
    // 计算等待时间
    if nextTaskTime.After(now) {
        q.nextTaskTimer.Reset(nextTaskTime.Sub(now))
    } else {
        // 任务已到期，立即处理
        q.nextTaskTimer.Reset(0)
    }
}

func (q *memoryScheduledQueue) processReadyTasks() {
    now := q.timeSource.Now()
    
    // 处理所有到期的任务
    for !q.taskQueue.IsEmpty() {
        task := q.taskQueue.Peek()
        if task.GetVisibilityTime().After(now) {
            break
        }
        
        // 移除任务
        q.taskQueue.Remove()
        
        // 提交到调度器执行
        q.scheduler.Submit(task)
    }
    
    // 更新定时器
    q.updateTimer()
}
```

#### 2.4.4 使用场景

**MemoryTimer任务**：
- 短期定时器
- 不需要持久化的定时任务
- WorkflowTask推测性超时

---

### 2.5 SpeculativeWorkflowTaskTimeoutQueue（推测性超时队列）

**文件**: [service/history/queues/speculative_workflow_task_timeout_queue.go](file:///Users/bytedance/code/temporal/service/history/queues/speculative_workflow_task_timeout_queue.go)

#### 2.5.1 数据结构

```go
type SpeculativeWorkflowTaskTimeoutQueue struct {
    timeoutQueue     *memoryScheduledQueue  // 组合内存队列
    executor         Executor               // 任务执行器
    priorityAssigner PriorityAssigner       // 优先级分配器
    
    namespaceRegistry namespace.Registry
    clusterMetadata   cluster.Metadata
    timeSource        clock.TimeSource
    metricsHandler    metrics.Handler
    logger            log.SnTaggedLogger
}
```

#### 2.5.2 核心特性

**推测性执行**：
- 用于处理WorkflowTask的超时
- 在Worker可能卡住时提前触发超时
- 提高系统响应速度

**组合模式**：
- 内部使用memoryScheduledQueue
- 添加特定的任务过滤和处理逻辑

#### 2.5.3 工作流程

```go
func NewSpeculativeWorkflowTaskTimeoutQueue(
    scheduler ctasks.Scheduler[ctasks.Task],
    priorityAssigner PriorityAssigner,
    executor Executor,
    namespaceRegistry namespace.Registry,
    clusterMetadata cluster.Metadata,
    timeSource clock.TimeSource,
    metricsHandler metrics.Handler,
    logger log.SnTaggedLogger,
) *SpeculativeWorkflowTaskTimeoutQueue {
    
    // 创建内存定时队列
    timeoutQueue := newMemoryScheduledQueue(
        scheduler,
        timeSource,
        logger,
        metricsHandler,
    )
    
    return &SpeculativeWorkflowTaskTimeoutQueue{
        timeoutQueue:      timeoutQueue,
        executor:          executor,
        priorityAssigner:  priorityAssigner,
        namespaceRegistry: namespaceRegistry,
        clusterMetadata:   clusterMetadata,
        timeSource:        timeSource,
        metricsHandler:    metricsHandler,
        logger:            logger,
    }
}

func (q SpeculativeWorkflowTaskTimeoutQueue) NotifyNewTasks(ts []tasks.Task) {
    for _, task := range ts {
        // 只处理WorkflowTaskTimeoutTask
        if wttt, ok := task.(*tasks.WorkflowTaskTimeoutTask); ok {
            // 创建可执行任务
            executable := newSpeculativeWorkflowTaskTimeoutExecutable(
                NewExecutable(
                    0,
                    wttt,
                    q.executor,
                    nil,
                    nil,
                    q.priorityAssigner,
                    q.timeSource,
                    q.namespaceRegistry,
                    q.clusterMetadata,
                    q.logger,
                    q.metricsHandler.WithTags(defaultExecutableMetricsTags...),
                ),
                wttt,
            )
            
            // 添加到内存队列
            q.timeoutQueue.Add(executable)
        }
    }
}

func (q SpeculativeWorkflowTaskTimeoutQueue) Category() tasks.Category {
    return tasks.CategoryMemoryTimer
}

func (q SpeculativeWorkflowTaskTimeoutQueue) FailoverNamespace(_ string) {
    // 推测性超时队列不支持故障转移
    // 因为这些任务是临时的、内存中的
}
```

#### 2.5.4 使用场景

**WorkflowTask超时处理**：
- 检测Worker是否卡住
- 提前触发超时，避免长时间等待
- 提高系统可用性

---

## 3. 辅助数据结构

### 3.1 Reader（读取器）

**文件**: [service/history/queues/reader.go](file:///Users/bytedance/code/temporal/service/history/queues/reader.go)

#### 3.1.1 接口定义

```go
type Reader interface {
    Scopes() []Scope
    
    // Slice管理
    WalkSlices(SliceIterator)
    SplitSlices(SliceSplitter)
    MergeSlices(...Slice)
    AppendSlices(...Slice)
    ClearSlices(SlicePredicate)
    CompactSlices(SlicePredicate)
    ShrinkSlices() int
    
    // 生命周期
    Notify()
    Pause(time.Duration)
    Start()
    Stop()
}
```

#### 3.1.2 数据结构

```go
type ReaderImpl struct {
    sync.Mutex
    
    readerID       int64
    options        *ReaderOptions
    scheduler      Scheduler
    rescheduler    Rescheduler
    timeSource     clock.TimeSource
    ratelimiter    quotas.RequestRateLimiter
    monitor        Monitor
    completionFn   ReaderCompletionFn
    logger         log.Logger
    metricsHandler metrics.Handler
    
    status     int32
    shutdownCh chan struct{}
    shutdownWG sync.WaitGroup
    
    // Slice管理
    slices *list.List  // 使用链表存储Slice
    
    // 状态跟踪
    pendingTasksCount int64
    paused            bool
}
```

#### 3.1.3 核心职责

**1. Slice管理**
```go
// 添加Slice
func (r *ReaderImpl) AppendSlices(slices ...Slice) {
    r.Lock()
    defer r.Unlock()
    
    for _, slice := range slices {
        r.slices.PushBack(slice)
    }
}

// 合并Slice
func (r *ReaderImpl) MergeSlices(slices ...Slice) {
    r.Lock()
    defer r.Unlock()
    
    if r.slices.Len() == 0 {
        for _, slice := range slices {
            r.slices.PushBack(slice)
        }
        return
    }
    
    // 合并到最后一个Slice
    lastElement := r.slices.Back()
    lastSlice := lastElement.Value.(Slice)
    
    for _, newSlice := range slices {
        if lastSlice.CanMergeWithSlice(newSlice) {
            mergedSlices := lastSlice.MergeWithSlice(newSlice)
            if len(mergedSlices) == 1 {
                lastSlice = mergedSlices[0]
                lastElement.Value = lastSlice
            } else {
                r.slices.PushBack(newSlice)
                lastSlice = newSlice
                lastElement = r.slices.Back()
            }
        } else {
            r.slices.PushBack(newSlice)
            lastSlice = newSlice
            lastElement = r.slices.Back()
        }
    }
}

// 收缩Slice
func (r *ReaderImpl) ShrinkSlices() int {
    r.Lock()
    defer r.Unlock()
    
    var tasksCompleted int
    var next *list.Element
    
    for e := r.slices.Front(); e != nil; e = next {
        next = e.Next()
        slice := e.Value.(Slice)
        
        // 收缩Slice的范围
        completed := slice.ShrinkScope()
        tasksCompleted += completed
        
        // 如果Slice已空，移除
        if !slice.MoreTasks() {
            slice.Clear()
            r.slices.Remove(e)
        }
    }
    
    return tasksCompleted
}
```

**2. 任务加载**
```go
func (r *ReaderImpl) processSlices() {
    r.Lock()
    defer r.Unlock()
    
    if r.paused {
        return
    }
    
    // 遍历所有Slice
    for e := r.slices.Front(); e != nil; e = e.Next() {
        slice := e.Value.(Slice)
        
        // 检查是否超过最大待处理任务数
        if r.pendingTasksCount >= int64(r.options.MaxPendingTasksCount()) {
            break
        }
        
        // 从Slice选择任务
        tasks, err := slice.SelectTasks(r.readerID, r.options.BatchSize())
        if err != nil {
            r.logger.Error("Failed to select tasks", tag.Error(err))
            continue
        }
        
        // 提交任务到调度器
        for _, task := range tasks {
            r.scheduler.Submit(task)
            r.pendingTasksCount++
        }
    }
}
```

**3. 速率限制**
```go
func (r *ReaderImpl) notify() {
    // 检查速率限制
    if !r.ratelimiter.Allow() {
        // 被限流，暂停一段时间
        r.Pause(throttleRetryDelay)
        return
    }
    
    // 处理Slice
    r.processSlices()
}
```

#### 3.1.4 Reader的作用

**并行处理**：
- 一个Queue可以有多个Reader
- 每个Reader独立处理自己的Slice
- 提高任务处理并发度

**隔离性**：
- 不同命名空间的任务可以在不同Reader中处理
- 避免相互影响

**负载均衡**：
- 根据负载动态调整Reader数量
- 自动平衡任务分配

---

### 3.2 Slice（切片）

**文件**: [service/history/queues/slice.go](file:///Users/bytedance/code/temporal/service/history/queues/slice.go)

#### 3.2.1 接口定义

```go
type Slice interface {
    Scope() Scope
    
    // 范围操作
    CanSplitByRange(tasks.Key) bool
    SplitByRange(tasks.Key) (left Slice, right Slice)
    
    // 谓词操作
    SplitByPredicate(tasks.Predicate) (pass Slice, fail Slice)
    
    // 合并操作
    CanMergeWithSlice(Slice) bool
    MergeWithSlice(slice Slice) []Slice
    CompactWithSlice(slice Slice) Slice
    
    // 收缩
    ShrinkScope() int
    
    // 任务选择
    SelectTasks(readerID int64, batchSize int) ([]Executable, error)
    MoreTasks() bool
    TaskStats() TaskStats
    
    Clear()
}
```

#### 3.2.2 数据结构

```go
type SliceImpl struct {
    paginationFnProvider PaginationFnProvider
    executableFactory    ExecutableFactory
    
    destroyed bool
    
    scope     Scope
    iterators []Iterator  // 任务迭代器列表
    
    *executableTracker  // 可执行任务跟踪器
    monitor Monitor
    
    maxPredicateSizeFn func() int
}
```

#### 3.2.3 核心职责

**1. 任务加载**
```go
func (s *SliceImpl) SelectTasks(readerID int64, batchSize int) ([]Executable, error) {
    var tasks []Executable
    
    // 从迭代器加载任务
    for len(tasks) < batchSize && len(s.iterators) > 0 {
        iterator := s.iterators[0]
        
        // 从迭代器获取下一个任务
        hasMore, err := iterator.Next()
        if err != nil {
            return nil, err
        }
        
        if !hasMore {
            // 迭代器已耗尽，移除
            s.iterators = s.iterators[1:]
            continue
        }
        
        task := iterator.Task()
        
        // 检查任务是否在Scope内
        if !s.scope.Contains(task) {
            continue
        }
        
        // 创建可执行任务
        executable := s.executableFactory(
            readerID,
            task,
        )
        
        tasks = append(tasks, executable)
        
        // 跟踪任务
        s.trackExecutable(executable)
    }
    
    return tasks, nil
}
```

**2. 范围分割**
```go
func (s *SliceImpl) SplitByRange(key tasks.Key) (left Slice, right Slice) {
    if !s.CanSplitByRange(key) {
        panic("Unable to split slice by range")
    }
    
    // 分割Scope
    leftScope, rightScope := s.scope.SplitByRange(key)
    
    // 创建两个新的Slice
    leftSlice := NewSlice(
        s.paginationFnProvider,
        s.executableFactory,
        s.monitor,
        leftScope,
        s.grouper,
        s.maxPredicateSizeFn,
    )
    
    rightSlice := NewSlice(
        s.paginationFnProvider,
        s.executableFactory,
        s.monitor,
        rightScope,
        s.grouper,
        s.maxPredicateSizeFn,
    )
    
    return leftSlice, rightSlice
}
```

**3. 谓词分割**
```go
func (s *SliceImpl) SplitByPredicate(predicate tasks.Predicate) (pass Slice, fail Slice) {
    // 分割Scope
    passScope, failScope := s.scope.SplitByPredicate(predicate)
    
    // 创建两个新的Slice
    passSlice := NewSlice(
        s.paginationFnProvider,
        s.executableFactory,
        s.monitor,
        passScope,
        s.grouper,
        s.maxPredicateSizeFn,
    )
    
    failSlice := NewSlice(
        s.paginationFnProvider,
        s.executableFactory,
        s.monitor,
        failScope,
        s.grouper,
        s.maxPredicateSizeFn,
    )
    
    return passSlice, failSlice
}
```

**4. 范围收缩**
```go
func (s *SliceImpl) ShrinkScope() int {
    // 获取任务统计
    stats := s.TaskStats()
    
    // 如果没有待处理任务，收缩范围
    if len(stats.PendingPerKey) == 0 {
        return 0
    }
    
    // 找到最小的待处理Key
    var minKey tasks.Key
    for key := range stats.PendingPerKey {
        if minKey.IsZero() || key.Less(minKey) {
            minKey = key
        }
    }
    
    // 收缩范围
    if s.scope.Range.InclusiveMin.Less(minKey) {
        s.scope.Range.InclusiveMin = minKey
        return 1
    }
    
    return 0
}
```

#### 3.2.4 Slice的作用

**任务分组**：
- 将任务按范围和谓词分组
- 支持高效的任务过滤

**动态调整**：
- 根据任务分布动态分割
- 支持负载均衡

**内存优化**：
- 自动收缩范围
- 释放已处理任务的内存

---

### 3.3 Scope（范围）

**文件**: [service/history/queues/scope.go](file:///Users/bytedance/code/temporal/service/history/queues/scope.go)

#### 3.3.1 数据结构

```go
type Scope struct {
    Range     Range            // 任务范围
    Predicate tasks.Predicate  // 任务谓词（过滤器）
}
```

#### 3.3.2 核心方法

```go
// 检查任务是否在Scope内
func (s *Scope) Contains(task tasks.Task) bool {
    return s.Range.ContainsKey(task.GetKey()) &&
        s.Predicate.Test(task)
}

// 按范围分割
func (s *Scope) SplitByRange(key tasks.Key) (left Scope, right Scope) {
    if !s.CanSplitByRange(key) {
        panic("Unable to split scope")
    }
    
    leftRange, rightRange := s.Range.Split(key)
    return NewScope(leftRange, s.Predicate), NewScope(rightRange, s.Predicate)
}

// 按谓词分割
func (s *Scope) SplitByPredicate(predicate tasks.Predicate) (pass Scope, fail Scope) {
    passScope := NewScope(
        s.Range,
        tasks.AndPredicates(s.Predicate, predicate),
    )
    failScope := NewScope(
        s.Range,
        predicates.And(
            s.Predicate,
            predicates.Not(predicate),
        ),
    )
    return passScope, failScope
}

// 按范围合并
func (s *Scope) CanMergeByRange(incomingScope Scope) bool {
    return s.Range.CanMerge(incomingScope.Range) &&
        s.Predicate.Equals(incomingScope.Predicate)
}

func (s *Scope) MergeByRange(incomingScope Scope) Scope {
    if !s.CanMergeByRange(incomingScope) {
        panic("Unable to merge scope")
    }
    
    mergedRange := s.Range.Merge(incomingScope.Range)
    return NewScope(mergedRange, s.Predicate)
}
```

#### 3.3.3 Scope的作用

**双重过滤**：
- Range：按任务Key范围过滤
- Predicate：按任务属性过滤

**示例**：
```go
// 创建一个Scope，只处理特定命名空间和时间范围内的任务
scope := NewScope(
    Range{
        InclusiveMin: tasks.NewKey(time.Now().Add(-1*time.Hour), 0),
        ExclusiveMax: tasks.NewKey(time.Now(), 0),
    },
    predicates.NamespaceID("my-namespace"),
)
```

---

### 3.4 Executable（可执行任务）

**文件**: [service/history/queues/executable.go](file:///Users/bytedance/code/temporal/service/history/queues/executable.go)

#### 3.4.1 接口定义

```go
type Executable interface {
    ctasks.Task       // 通用任务接口
    tasks.Task        // History任务接口
    
    Attempt() int
    GetTask() tasks.Task
    GetPriority() ctasks.Priority
    GetScheduledTime() time.Time
    SetScheduledTime(time.Time)
}
```

#### 3.4.2 数据结构

```go
type executableImpl struct {
    tasks.Task
    
    attempt       int
    priority      ctasks.Priority
    scheduledTime time.Time
    
    executor         Executor
    priorityAssigner PriorityAssigner
    
    timeSource        clock.TimeSource
    namespaceRegistry namespace.Registry
    clusterMetadata   cluster.Metadata
    logger            log.Logger
    metricsHandler    metrics.Handler
}
```

#### 3.4.3 核心方法

**执行任务**：
```go
func (e *executableImpl) Execute() error {
    ctx, cancel := newTaskIOContext()
    defer cancel()
    
    // 执行任务
    response := e.executor.Execute(ctx, e)
    
    // 处理执行结果
    if response.ExecutionErr != nil {
        // 任务执行失败
        if e.isRetryableError(response.ExecutionErr) {
            // 可重试错误，重新调度
            return response.ExecutionErr
        }
        
        // 不可重试错误，发送到DLQ
        e.sendToDLQ(response.ExecutionErr)
        return nil
    }
    
    // 任务执行成功
    return nil
}

func (e *executableImpl) HandleErr(err error) {
    // 处理执行错误
    if e.isRetryableError(err) {
        // 重新调度
        e.reschedule()
    } else {
        // 发送到DLQ
        e.sendToDLQ(err)
    }
}
```

**优先级管理**：
```go
func (e *executableImpl) GetPriority() ctasks.Priority {
    // 根据任务类型和属性分配优先级
    return e.priorityAssigner.AssignPriority(e.Task)
}
```

#### 3.4.4 Executable的作用

**任务包装**：
- 将原始Task包装为可执行单元
- 添加执行上下文和元数据

**执行管理**：
- 管理任务执行生命周期
- 处理重试和错误

**优先级调度**：
- 根据任务属性分配优先级
- 支持优先级队列调度

---

### 3.5 Iterator（迭代器）

**文件**: [service/history/queues/iterator.go](file:///Users/bytedance/code/temporal/service/history/queues/iterator.go)

#### 3.5.1 数据结构

```go
type Iterator interface {
    Next() (bool, error)
    Task() tasks.Task
}

type IteratorImpl struct {
    paginationFn PaginationFn[tasks.Task]
    range        Range
    
    currentPageToken []byte
    currentTasks     []tasks.Task
    currentIndex     int
    exhausted        bool
}
```

#### 3.5.2 工作流程

```go
func (i *IteratorImpl) Next() (bool, error) {
    // 如果当前批次还有任务
    if i.currentIndex < len(i.currentTasks) {
        i.currentIndex++
        return true, nil
    }
    
    // 如果已经耗尽
    if i.exhausted {
        return false, nil
    }
    
    // 加载下一批次
    tasks, nextPageToken, err := i.paginationFn(i.currentPageToken)
    if err != nil {
        return false, err
    }
    
    if len(tasks) == 0 {
        i.exhausted = true
        return false, nil
    }
    
    i.currentTasks = tasks
    i.currentIndex = 0
    i.currentPageToken = nextPageToken
    
    if nextPageToken == nil {
        i.exhausted = true
    }
    
    return true, nil
}

func (i *IteratorImpl) Task() tasks.Task {
    if i.currentIndex >= len(i.currentTasks) {
        return nil
    }
    return i.currentTasks[i.currentIndex]
}
```

#### 3.5.3 Iterator的作用

**分页加载**：
- 从数据库分页加载任务
- 避免一次性加载大量数据

**惰性求值**：
- 按需加载任务
- 减少内存占用

---

## 4. 架构设计分析

### 4.1 整体架构

```
┌─────────────────────────────────────────────────────────────┐
│                        Queue (接口)                          │
│  Category() | NotifyNewTasks() | FailoverNamespace() |      │
│  Start() | Stop()                                            │
└─────────────────────────────────────────────────────────────┘
                          │
        ┌─────────────────┼─────────────────┐
        │                 │                 │
        ▼                 ▼                 ▼
┌──────────────┐  ┌──────────────┐  ┌──────────────┐
│immediateQueue│  │scheduledQueue│  │memoryQueue   │
│              │  │              │  │              │
│ - notifyCh   │  │ - timerGate  │  │ - taskQueue  │
│              │  │ - lookAhead  │  │ - timer      │
└──────────────┘  └──────────────┘  └──────────────┘
        │                 │
        └────────┬────────┘
                 │
                 ▼
        ┌─────────────────┐
        │   queueBase     │
        │                 │
        │ - readerGroup   │
        │ - scheduler     │
        │ - monitor       │
        └─────────────────┘
                 │
                 ▼
        ┌─────────────────┐
        │   ReaderGroup   │
        │                 │
        │ - readers       │
        └─────────────────┘
                 │
                 ▼
        ┌─────────────────┐
        │     Reader      │
        │                 │
        │ - slices        │
        │ - ratelimiter   │
        └─────────────────┘
                 │
                 ▼
        ┌─────────────────┐
        │     Slice       │
        │                 │
        │ - scope         │
        │ - iterators     │
        │ - tracker       │
        └─────────────────┘
                 │
                 ▼
        ┌─────────────────┐
        │   Executable    │
        │                 │
        │ - task          │
        │ - executor      │
        │ - priority      │
        └─────────────────┘
```

### 4.2 数据流转

```
1. 任务创建
   Workflow状态变更 → 创建Task → NotifyNewTasks()

2. 任务加载
   Queue → Reader → Slice → Iterator → Database

3. 任务执行
   Iterator → Executable → Scheduler → Executor

4. 状态更新
   Executor → Task完成 → ShrinkScope → Checkpoint
```

### 4.3 设计模式

#### 4.3.1 组合模式

```go
// Queue组合queueBase
type immediateQueue struct {
    *queueBase
    notifyCh chan struct{}
}

// SpeculativeWorkflowTaskTimeoutQueue组合memoryScheduledQueue
type SpeculativeWorkflowTaskTimeoutQueue struct {
    timeoutQueue *memoryScheduledQueue
    // ...
}
```

#### 4.3.2 策略模式

```go
// 不同的队列实现不同的处理策略
type Queue interface {
    NotifyNewTasks(tasks []tasks.Task)
}

// immediateQueue：立即处理
func (p *immediateQueue) NotifyNewTasks(tasks []tasks.Task) {
    p.notify()  // 立即通知
}

// scheduledQueue：延迟处理
func (p *scheduledQueue) NotifyNewTasks(tasks []tasks.Task) {
    // 找到最早时间，设置定时器
    p.notify(newTime)
}
```

#### 4.3.3 迭代器模式

```go
type Iterator interface {
    Next() (bool, error)
    Task() tasks.Task
}

// 支持分页加载
for hasMore, err := iterator.Next(); hasMore; hasMore, err = iterator.Next() {
    task := iterator.Task()
    // 处理任务
}
```

#### 4.3.4 观察者模式

```go
// Queue作为被观察者
func (p *immediateQueue) NotifyNewTasks(tasks []tasks.Task) {
    p.notify()  // 通知观察者（processEventLoop）
}

// processEventLoop作为观察者
func (p *immediateQueue) processEventLoop() {
    for {
        select {
        case <-p.notifyCh:
            // 收到通知，处理任务
            p.processNewRange()
        }
    }
}
```

### 4.4 性能优化

#### 4.4.1 分页加载

```go
// Iterator分页加载任务
func (i *IteratorImpl) Next() (bool, error) {
    if i.currentIndex < len(i.currentTasks) {
        return true, nil
    }
    
    // 加载下一批次
    tasks, nextPageToken, err := i.paginationFn(i.currentPageToken)
    // ...
}
```

**优点**：
- 减少内存占用
- 避免一次性加载大量数据
- 支持流式处理

#### 4.4.2 优先队列

```go
// memoryScheduledQueue使用优先队列
func executableVisibilityTimeCompareLess(this, that Executable) bool {
    return this.GetVisibilityTime().Before(that.GetVisibilityTime())
}

taskQueue := collection.NewPriorityQueue[Executable](executableVisibilityTimeCompareLess)
```

**优点**：
- 自动按时间排序
- 高效获取最早任务
- O(log n)插入和删除

#### 4.4.3 动态调整

```go
// 定期强制创建新Slice
if now := p.timeSource.Now(); now.After(p.nextForceNewSliceTime) {
    reader.AppendSlices(slices...)
    p.nextForceNewSliceTime = now.Add(forceNewSliceDuration)
} else {
    reader.MergeSlices(slices...)
}
```

**优点**：
- 避免单个Slice无限增长
- 支持动态负载均衡
- 提高并发处理能力

#### 4.4.4 检查点机制

```go
func (p *queueBase) checkpoint() {
    // 1. 收缩Slice
    tasksCompleted := r.ShrinkSlices()
    
    // 2. 持久化状态
    p.shard.UpdateQueueState(p.category, state)
}
```

**优点**：
- 减少重启后的任务重复加载
- 快速恢复处理进度
- 提高系统可靠性

---

## 5. 总结

### 5.1 核心实现对比

| 实现 | 继承关系 | 存储位置 | 任务类型 | 特点 |
|------|---------|---------|---------|------|
| **immediateQueue** | queueBase | 数据库 | Transfer, Replication, Visibility, Outbound | 立即执行，事件驱动 |
| **scheduledQueue** | queueBase | 数据库 | Timer, Archival | 定时执行，TimerGate管理 |
| **memoryScheduledQueue** | 无 | 内存 | MemoryTimer | 纯内存，优先队列 |
| **SpeculativeWorkflowTaskTimeoutQueue** | 组合memoryScheduledQueue | 内存 | WorkflowTaskTimeout | 推测性超时，特殊处理 |

### 5.2 辅助结构作用

| 结构 | 作用 | 关键特性 |
|------|------|---------|
| **Reader** | 任务读取器 | 管理Slice，并行处理 |
| **Slice** | 任务切片 | 范围和谓词过滤，动态调整 |
| **Scope** | 任务范围 | Range + Predicate双重过滤 |
| **Executable** | 可执行任务 | 任务包装，执行管理 |
| **Iterator** | 任务迭代器 | 分页加载，惰性求值 |

### 5.3 设计亮点

1. **分层架构**：Queue → Reader → Slice → Executable，职责清晰
2. **组合模式**：灵活组合不同实现，代码复用性高
3. **事件驱动**：通过channel通知，避免轮询浪费
4. **动态调整**：支持运行时分割、合并、收缩
5. **性能优化**：分页加载、优先队列、检查点机制

这个架构设计充分体现了Temporal的核心设计理念：**高性能、高可靠、可扩展**。
