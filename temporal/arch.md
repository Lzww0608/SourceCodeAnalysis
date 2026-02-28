# Temporal 源码与架构深度分析

## 目录

1. [源码目录结构与模块划分](#1-源码目录结构与模块划分)
2. [核心服务组件架构](#2-核心服务组件架构)
3. [Worker组件与任务处理机制](#3-worker组件与任务处理机制)
4. [关键数据结构与算法](#4-关键数据结构与算法)
5. [持久化存储与状态管理](#5-持久化存储与状态管理)
6. [核心流程实现机制](#6-核心流程实现机制)

---

## 1. 源码目录结构与模块划分

### 1.1 顶层目录结构

```
temporal/
├── api/                    # Protobuf定义的API接口
│   ├── adminservice/       # 管理服务API
│   ├── history/            # History事件消息定义
│   ├── enums/              # 枚举类型定义
│   ├── persistence/        # 持久化数据结构
│   └── workflow/           # Workflow相关消息定义
├── client/                 # 客户端SDK
│   ├── frontend/           # Frontend服务客户端
│   ├── history/            # History服务客户端
│   └── matching/           # Matching服务客户端
├── cmd/                    # 命令行工具入口
│   ├── server/             # Temporal服务器主程序
│   └── tools/              # 辅助工具
├── common/                 # 公共库与工具
│   ├── cache/              # 缓存实现
│   ├── clock/              # 时钟抽象
│   ├── collection/         # 集合工具
│   ├── log/                # 日志系统
│   ├── metrics/            # 指标监控
│   ├── namespace/          # 命名空间管理
│   ├── persistence/        # 持久化抽象层
│   ├── rpc/                # RPC工具
│   └── tasks/              # 任务调度框架
├── service/                # 核心服务实现
│   ├── frontend/           # Frontend服务
│   ├── history/            # History服务
│   ├── matching/           # Matching服务
│   └── worker/             # 内部Worker服务
├── temporal/               # 服务器启动与配置
├── tests/                  # 测试用例
└── tools/                  # 运维工具
```

### 1.2 核心模块职责

| 模块 | 路径 | 核心职责 |
|------|------|---------|
| **API层** | `api/` | 定义所有gRPC接口、消息格式、枚举类型 |
| **Client层** | `client/` | 实现各服务的客户端，提供RPC调用能力 |
| **Service层** | `service/` | 实现三大核心服务：Frontend、History、Matching |
| **Persistence层** | `common/persistence/` | 抽象存储接口，支持Cassandra/MySQL/PostgreSQL/SQLite |
| **Worker层** | `service/worker/` | 内部Worker，执行系统级Workflow和Activity |

---

## 2. 核心服务组件架构

### 2.1 History Service

**源码路径**: [service/history/](file:///Users/bytedance/code/temporal/service/history/)

#### 2.1.1 架构设计

History Service是Temporal的核心服务，负责管理所有Workflow Execution的生命周期。

```
┌─────────────────────────────────────────────────────────┐
│                   History Service                        │
│  ┌──────────────────────────────────────────────────┐  │
│  │              Shard Engine (Engine)                │  │
│  │  ┌────────────────────────────────────────────┐  │  │
│  │  │         Workflow Context                    │  │  │
│  │  │  ┌──────────────────────────────────────┐  │  │  │
│  │  │  │      Mutable State                   │  │  │  │
│  │  │  │  - Workflow Execution State          │  │  │  │
│  │  │  │  - Activity Info                     │  │  │  │
│  │  │  │  - Timer Info                        │  │  │  │
│  │  │  │  - Child Workflow Info               │  │  │  │
│  │  │  └──────────────────────────────────────┘  │  │  │
│  │  └────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────┐  │
│  │           Queue Processors                        │  │
│  │  ┌─────────────┐  ┌─────────────┐               │  │
│  │  │ Transfer    │  │ Timer       │               │  │
│  │  │ Queue       │  │ Queue       │               │  │
│  │  └─────────────┘  └─────────────┘               │  │
│  └──────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

#### 2.1.2 核心接口定义

**文件**: [service/history/shard/engine.go](file:///Users/bytedance/code/temporal/service/history/shard/engine.go)

```go
type Engine interface {
    // Workflow生命周期管理
    StartWorkflowExecution(ctx context.Context, request *historyservice.StartWorkflowExecutionRequest) (*historyservice.StartWorkflowExecutionResponse, error)
    GetMutableState(ctx context.Context, request *historyservice.GetMutableStateRequest) (*historyservice.GetMutableStateResponse, error)
    
    // Workflow Task处理
    RecordWorkflowTaskStarted(ctx context.Context, request *historyservice.RecordWorkflowTaskStartedRequest) (*historyservice.RecordWorkflowTaskStartedResponse, error)
    RespondWorkflowTaskCompleted(ctx context.Context, request *historyservice.RespondWorkflowTaskCompletedRequest) (*historyservice.RespondWorkflowTaskCompletedResponse, error)
    
    // Activity Task处理
    RecordActivityTaskStarted(ctx context.Context, request *historyservice.RecordActivityTaskStartedRequest) (*historyservice.RecordActivityTaskStartedResponse, error)
    RespondActivityTaskCompleted(ctx context.Context, request *historyservice.RespondActivityTaskCompletedRequest) (*historyservice.RespondActivityTaskCompletedResponse, error)
    
    // Workflow控制
    SignalWorkflowExecution(ctx context.Context, request *historyservice.SignalWorkflowExecutionRequest) (*historyservice.SignalWorkflowExecutionResponse, error)
    TerminateWorkflowExecution(ctx context.Context, request *historyservice.TerminateWorkflowExecutionRequest) (*historyservice.TerminateWorkflowExecutionResponse, error)
    ResetWorkflowExecution(ctx context.Context, request *historyservice.ResetWorkflowExecutionRequest) (*historyservice.ResetWorkflowExecutionResponse, error)
    
    // 服务生命周期
    Start()
    Stop()
}
```

#### 2.1.3 Workflow Context实现

**文件**: [service/history/workflow/context.go](file:///Users/bytedance/code/temporal/service/history/workflow/context.go)

Workflow Context是管理单个Workflow Execution状态的核心抽象：

```go
type Context interface {
    GetWorkflowKey() definition.WorkflowKey
    
    // 状态加载与持久化
    LoadMutableState(ctx context.Context, shardContext shard.Context) (MutableState, error)
    Clear()
    
    // 锁机制
    Lock(ctx context.Context, lockPriority locks.Priority) error
    Unlock()
    
    // 状态持久化
    CreateWorkflowExecution(ctx context.Context, shardContext shard.Context, ...) error
    UpdateWorkflowExecutionAsActive(ctx context.Context, shardContext shard.Context) error
    UpdateWorkflowExecutionAsPassive(ctx context.Context, shardContext shard.Context) error
    
    // 事件持久化
    PersistWorkflowEvents(ctx context.Context, shardContext shard.Context, ...) (int64, error)
}
```

#### 2.1.4 Queue处理机制

**文件**: [service/history/queues/queue.go](file:///Users/bytedance/code/temporal/service/history/queues/queue.go)

History Service维护多个内部队列处理器：

```go
type Queue interface {
    Category() tasks.Category
    NotifyNewTasks(tasks []tasks.Task)
    FailoverNamespace(namespaceID string)
    Start()
    Stop()
}
```

**队列类型**：

| 队列类型 | 职责 | 触发条件 |
|---------|------|---------|
| **Transfer Queue** | 向Matching Service发送任务 | Workflow状态变更 |
| **Timer Queue** | 处理定时器和超时 | Timer到期 |
| **Replication Queue** | 跨集群复制 | 状态变更 |
| **Visibility Queue** | 更新可见性索引 | Workflow事件 |

---

### 2.2 Matching Service

**源码路径**: [service/matching/](file:///Users/bytedance/code/temporal/service/matching/)

#### 2.2.1 架构设计

Matching Service负责Task Queue管理和任务分发。

```
┌─────────────────────────────────────────────────────────┐
│                   Matching Service                       │
│  ┌──────────────────────────────────────────────────┐  │
│  │            Task Queue Manager                     │  │
│  │  ┌────────────────────────────────────────────┐  │  │
│  │  │         Task Matcher                       │  │  │
│  │  │  ┌──────────────┐  ┌──────────────┐       │  │  │
│  │  │  │  taskC       │  │  queryTaskC  │       │  │  │
│  │  │  │  (channel)   │  │  (channel)   │       │  │  │
│  │  │  └──────────────┘  └──────────────┘       │  │  │
│  │  └────────────────────────────────────────────┘  │  │
│  └──────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────┐  │
│  │           Forwarder (分区转发)                    │  │
│  └──────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────┐  │
│  │           Persistence (Task存储)                  │  │
│  │  ┌─────────────┐  ┌─────────────┐               │  │
│  │  │ TaskReader  │  │ TaskWriter  │               │  │
│  │  └─────────────┘  └─────────────┘               │  │
│  └──────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

#### 2.2.2 Task Matcher核心实现

**文件**: [service/matching/matcher.go](file:///Users/bytedance/code/temporal/service/matching/matcher.go)

Task Matcher是Matching Service的核心组件，实现任务的生产者-消费者匹配：

```go
type TaskMatcher struct {
    config *taskQueueConfig
    
    // 同步任务channel - 匹配生产者/消费者
    taskC chan *internalTask
    // 查询任务channel
    queryTaskC chan *internalTask
    // 关闭channel
    closeC chan struct{}
    
    // 速率限制器
    dynamicRateBurst       quotas.MutableRateBurst
    dynamicRateLimiter     *quotas.DynamicRateLimiterImpl
    rateLimiter            quotas.RateLimiter
    
    // 转发器（用于分区转发）
    fwdr                   *Forwarder
    
    // 监控指标
    metricsHandler         metrics.Handler
    numPartitions          func() int
    backlogTasksCreateTime map[int64]int
    backlogTasksLock       sync.Mutex
    lastPoller             atomic.Int64
}
```

#### 2.2.3 任务匹配算法

**Offer方法** - 任务生产者调用：

```go
func (tm *TaskMatcher) Offer(ctx context.Context, task *internalTask) (bool, error) {
    // 1. 检查积压任务数量
    if !tm.isBacklogNegligible() {
        return false, nil
    }
    
    // 2. 速率限制检查（非转发任务）
    if !task.isForwarded() {
        if err := tm.rateLimiter.Wait(ctx); err != nil {
            return false, err
        }
    }
    
    // 3. 尝试同步匹配（直接发送给等待的poller）
    select {
    case tm.taskC <- task:
        if task.responseC != nil {
            err := <-task.responseC
            return true, err
        }
        return false, nil
    default:
        // 4. 无poller等待，尝试转发到父分区
        select {
        case token := <-tm.fwdrAddReqTokenC():
            if err := tm.fwdr.ForwardTask(ctx, task); err == nil {
                token.release()
                return false, nil
            }
        default:
            return false, nil
        }
    }
}
```

**Poll方法** - 任务消费者调用：

```go
func (tm *TaskMatcher) Poll(ctx context.Context, pollRequest *internalPollRequest) (*internalTask, error) {
    // 1. 更新最近poller时间戳
    tm.lastPoller.Store(time.Now().UnixNano())
    
    // 2. 尝试从channel获取任务
    select {
    case task := <-tm.taskC:
        return task, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    case <-tm.closeC:
        return nil, errors.New("task queue closed")
    }
}
```

#### 2.2.4 Task Queue分区机制

Matching Service将Task Queue分为多个分区以提高吞吐量：

```
                    Root Partition (Partition 0)
                           │
            ┌──────────────┼──────────────┐
            │              │              │
      Partition 1    Partition 2    Partition 3
```

**转发规则**：
- 当子分区无poller时，任务转发到父分区
- 当子分区有积压任务时，poller可转发到父分区
- 根分区强制加载时，所有子分区也必须加载

---

### 2.3 Frontend Service

**源码路径**: [service/frontend/](file:///Users/bytedance/code/temporal/service/frontend/)

#### 2.3.1 核心职责

Frontend Service作为API网关，负责：
- 请求路由与负载均衡
- 权限验证与授权
- 请求限流
- 跨集群路由

#### 2.3.2 请求处理流程

```
Client Request
     │
     ▼
┌─────────────────┐
│  Authentication │  身份验证
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Authorization  │  权限检查
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│   Rate Limit    │  限流控制
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│  Route Request  │  路由决策
└────────┬────────┘
         │
    ┌────┴────┐
    ▼         ▼
History    Matching
Service    Service
```

---

## 3. Worker组件与任务处理机制

### 3.1 Worker架构

**源码路径**: [service/worker/](file:///Users/bytedance/code/temporal/service/worker/)

Temporal Server内部运行多个Worker，执行系统级Workflow：

```
┌─────────────────────────────────────────────────────────┐
│                   Worker Manager                         │
│  ┌──────────────────────────────────────────────────┐  │
│  │              Default Worker                       │  │
│  │  Task Queue: temporal-system                     │  │
│  └──────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────┐  │
│  │              Worker Components                    │  │
│  │  ┌─────────────┐  ┌─────────────┐               │  │
│  │  │ Scheduler   │  │ Scanner     │               │  │
│  │  │ (定时任务)  │  │ (数据扫描)  │               │  │
│  │  └─────────────┘  └─────────────┘               │  │
│  │  ┌─────────────┐  ┌─────────────┐               │  │
│  │  │ Replicator  │  │ Batcher     │               │  │
│  │  │ (跨集群复制)│  │ (批处理)    │               │  │
│  │  └─────────────┘  └─────────────┘               │  │
│  │  ┌─────────────┐  ┌─────────────┐               │  │
│  │  │ Deployment  │  │ DeleteNS    │               │  │
│  │  │ (部署管理)  │  │ (命名空间删除)│              │  │
│  │  └─────────────┘  └─────────────┘               │  │
│  └──────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

### 3.2 Worker Manager实现

**文件**: [service/worker/worker.go](file:///Users/bytedance/code/temporal/service/worker/worker.go)

```go
type workerManager struct {
    status           int32
    hostInfo         membership.HostInfo
    logger           log.Logger
    sdkClientFactory sdk.ClientFactory
    workers          []sdkworker.Worker
    workerComponents []workercommon.WorkerComponent
}

func (wm *workerManager) Start() {
    // 1. 创建默认Worker选项
    defaultWorkerOptions := sdkworker.Options{
        Identity: "temporal-system@" + wm.hostInfo.Identity(),
        BackgroundActivityContext: headers.SetCallerType(
            context.Background(), 
            headers.CallerTypeBackground,
        ),
    }
    
    // 2. 创建SDK Client
    sdkClient := wm.sdkClientFactory.GetSystemClient()
    defaultWorker := wm.sdkClientFactory.NewWorker(
        sdkClient, 
        primitives.DefaultWorkerTaskQueue, 
        defaultWorkerOptions,
    )
    
    // 3. 注册Worker Components
    for _, wc := range wm.workerComponents {
        // 注册Workflow
        if wfWorkerOptions := wc.DedicatedWorkflowWorkerOptions(); wfWorkerOptions != nil {
            dedicatedWorker := wm.sdkClientFactory.NewWorker(...)
            wc.RegisterWorkflow(dedicatedWorker)
        } else {
            wc.RegisterWorkflow(defaultWorker)
        }
        
        // 注册Activity
        if activityWorkerOptions := wc.DedicatedActivityWorkerOptions(); activityWorkerOptions != nil {
            activityWorker := wm.sdkClientFactory.NewWorker(...)
            wc.RegisterActivities(activityWorker)
        } else {
            wc.RegisterActivities(defaultWorker)
        }
    }
    
    // 4. 启动所有Workers
    for _, w := range wm.workers {
        w.Start()
    }
}
```

### 3.3 Worker Components详解

#### 3.3.1 Scheduler（定时任务调度器）

**源码路径**: [service/worker/scheduler/](file:///Users/bytedance/code/temporal/service/worker/scheduler/)

**核心文件**：
- [spec.go](file:///Users/bytedance/code/temporal/service/worker/scheduler/spec.go) - 调度规格定义
- [workflow.go](file:///Users/bytedance/code/temporal/service/worker/scheduler/workflow.go) - 调度Workflow实现
- [activities.go](file:///Users/bytedance/code/temporal/service/worker/scheduler/activities.go) - 调度Activity实现

**功能**：
- 支持Cron表达式调度
- 支持日历调度
- 支持间隔调度
- 自动重试与错误处理

#### 3.3.2 Scanner（数据扫描器）

**源码路径**: [service/worker/scanner/](file:///Users/bytedance/code/temporal/service/worker/scanner/)

**核心文件**：
- [scanner.go](file:///Users/bytedance/code/temporal/service/worker/scanner/scanner.go) - 扫描器主逻辑
- [executor/executor.go](file:///Users/bytedance/code/temporal/service/worker/scanner/executor/executor.go) - 执行器

**功能**：
- 历史数据清理
- 执行记录验证
- 构建ID清理

#### 3.3.3 Replicator（跨集群复制器）

**源码路径**: [service/worker/replicator/](file:///Users/bytedance/code/temporal/service/worker/replicator/)

**核心文件**：
- [replicator.go](file:///Users/bytedance/code/temporal/service/worker/replicator/replicator.go) - 复制器实现

**功能**：
- 跨集群Workflow状态复制
- 命名空间复制
- 冲突检测与解决

### 3.4 任务处理流程

#### 3.4.1 Workflow Task处理流程

```
┌──────────┐    PollWorkflowTask     ┌──────────┐
│  Worker  │ ───────────────────────►│ Matching │
└──────────┘                         └──────────┘
     │                                     │
     │                                     │ RecordWorkflowTaskStarted
     │                                     ▼
     │                               ┌──────────┐
     │                               │  History │
     │                               └──────────┘
     │                                     │
     │    WorkflowTask (with History)      │
     │ ◄───────────────────────────────────┤
     │                                     │
     │ Execute Workflow Code               │
     │                                     │
     │ RespondWorkflowTaskCompleted        │
     │ ────────────────────────────────────►
     │    (Commands: ScheduleActivity,     │
     │     StartTimer, CompleteWorkflow)   │
     │                                     │
     │                                     ▼
     │                               ┌──────────┐
     │                               │  History │
     │                               │  Update  │
     │                               │  State   │
     │                               └──────────┘
```

#### 3.4.2 Activity Task处理流程

```
┌──────────┐    PollActivityTask      ┌──────────┐
│  Worker  │ ───────────────────────►│ Matching │
└──────────┘                         └──────────┘
     │                                     │
     │                                     │ RecordActivityTaskStarted
     │                                     ▼
     │                               ┌──────────┐
     │                               │  History │
     │                               └──────────┘
     │                                     │
     │    ActivityTask (with Input)        │
     │ ◄───────────────────────────────────┤
     │                                     │
     │ Execute Activity Code               │
     │                                     │
     │ RespondActivityTaskCompleted        │
     │ ────────────────────────────────────►
     │    (Result)                         │
     │                                     │
     │                                     ▼
     │                               ┌──────────┐
     │                               │  History │
     │                               │  Update  │
     │                               │  State   │
     │                               └──────────┘
```

---

## 4. 关键数据结构与算法

### 4.1 Mutable State

**源码路径**: [service/history/workflow/mutable_state.go](file:///Users/bytedance/code/temporal/service/history/workflow/mutable_state.go)

Mutable State是Workflow Execution的内存状态表示：

```go
type MutableState interface {
    // 历史事件管理
    AddHistoryEvent(t enumspb.EventType, setAttributes func(*historypb.HistoryEvent)) *historypb.HistoryEvent
    LoadHistoryEvent(ctx context.Context, token []byte) (*historypb.HistoryEvent, error)
    
    // Activity管理
    AddActivityTaskScheduledEvent(int64, *commandpb.ScheduleActivityTaskCommandAttributes, bool) (*historypb.HistoryEvent, *persistencespb.ActivityInfo, error)
    AddActivityTaskStartedEvent(*persistencespb.ActivityInfo, int64, string, ...) (*historypb.HistoryEvent, error)
    AddActivityTaskCompletedEvent(int64, int64, *workflowservice.RespondActivityTaskCompletedRequest) (*historypb.HistoryEvent, error)
    
    // Workflow Task管理
    AddWorkflowTaskScheduledEvent(bypassTaskGeneration bool, workflowTaskType enumsspb.WorkflowTaskType) (*WorkflowTaskInfo, error)
    AddWorkflowTaskStartedEvent(int64, string, *taskqueuepb.TaskQueue, ...) (*historypb.HistoryEvent, *WorkflowTaskInfo, error)
    AddWorkflowTaskCompletedEvent(*WorkflowTaskInfo, *workflowservice.RespondWorkflowTaskCompletedRequest, ...) (*historypb.HistoryEvent, error)
    
    // Timer管理
    AddTimerStartedEvent(int64, *commandpb.StartTimerCommandAttributes) (*historypb.HistoryEvent, *persistencespb.TimerInfo, error)
    AddTimerFiredEvent(int64) (*historypb.HistoryEvent, error)
    
    // Child Workflow管理
    AddStartChildWorkflowExecutionInitiatedEvent(int64, string, *commandpb.StartChildWorkflowExecutionCommandAttributes, namespace.ID) (*historypb.HistoryEvent, *persistencespb.ChildExecutionInfo, error)
    
    // Signal管理
    AddSignalExternalWorkflowExecutionInitiatedEvent(int64, string, *commandpb.SignalExternalWorkflowExecutionCommandAttributes, namespace.ID) (*historypb.HistoryEvent, *persistencespb.SignalInfo, error)
}
```

**核心数据结构**：

```go
type WorkflowTaskInfo struct {
    Version                  int64
    ScheduledEventID         int64
    StartedEventID           int64
    RequestID                string
    WorkflowTaskTimeout      time.Duration
    TaskQueue                *taskqueuepb.TaskQueue
    Attempt                  int32
    ScheduledTime            time.Time
    StartedTime              time.Time
    OriginalScheduledTime    time.Time
    Type                     enumsspb.WorkflowTaskType
    SuggestContinueAsNew     bool
    HistorySizeBytes         int64
}
```

### 4.2 Task抽象

**源码路径**: [service/history/tasks/task.go](file:///Users/bytedance/code/temporal/service/history/tasks/task.go)

```go
type Task interface {
    GetKey() Key
    GetNamespaceID() string
    GetWorkflowID() string
    GetRunID() string
    GetTaskID() int64
    GetVisibilityTime() time.Time
    GetCategory() Category
    GetType() enumsspb.TaskType
    
    SetTaskID(id int64)
    SetVisibilityTime(timestamp time.Time)
}
```

**Task Category分类**：

| Category | 用途 | 示例任务类型 |
|----------|------|------------|
| **Transfer** | 立即执行的任务 | ActivityTask, WorkflowTask |
| **Timer** | 定时任务 | UserTimer, ActivityTimeout |
| **Replication** | 跨集群复制 | HistoryReplication |
| **Visibility** | 可见性索引 | IndexWorkflow |
| **Archival** | 归档任务 | ArchiveHistory |

### 4.3 Internal Task

**源码路径**: [service/matching/task.go](file:///Users/bytedance/code/temporal/service/matching/task.go)

Matching Service内部任务表示：

```go
type internalTask struct {
    event            *genericTaskInfo   // Activity或Workflow任务
    query            *queryTaskInfo     // Query任务
    nexus            *nexusTaskInfo     // Nexus任务
    started          *startedTaskInfo   // 已启动任务（来自父分区）
    namespace        namespace.Name
    source           enumsspb.TaskSource
    responseC        chan error         // 同步匹配响应channel
    backlogCountHint func() int64
    forwardInfo      *taskqueuespb.TaskForwardInfo    // 转发信息
    redirectInfo     *taskqueuespb.BuildIdRedirectInfo // 重定向信息
    recycleToken     func()             // 令牌回收函数
}
```

### 4.4 分片算法

**文件**: [common/util.go](file:///Users/bytedance/code/temporal/common/util.go)

```go
func WorkflowIDToHistoryShard(namespaceID string, workflowID string, numShards int32) int32 {
    hash := fnv.New32a()
    hash.Write([]byte(namespaceID))
    hash.Write([]byte(workflowID))
    return int32(hash.Sum32() % uint32(numShards))
}
```

**算法特点**：
- 使用FNV-1a哈希算法
- 确保同一Workflow Execution始终路由到同一Shard
- 支持动态扩容（通过一致性哈希）

---

## 5. 持久化存储与状态管理

### 5.1 存储架构

```
┌─────────────────────────────────────────────────────────┐
│              Persistence Abstraction Layer               │
│  ┌──────────────────────────────────────────────────┐  │
│  │           Data Store Interface                    │  │
│  │  - ExecutionManager                               │  │
│  │  - TaskManager                                    │  │
│  │  - MetadataManager                                │  │
│  │  - VisibilityManager                              │  │
│  └──────────────────────────────────────────────────┘  │
│  ┌──────────────────────────────────────────────────┐  │
│  │           Implementation                          │  │
│  │  ┌──────────┐  ┌──────────┐  ┌──────────┐       │  │
│  │  │Cassandra │  │  MySQL   │  │PostgreSQL│       │  │
│  │  └──────────┘  └──────────┘  └──────────┘       │  │
│  │  ┌──────────┐                                    │  │
│  │  │  SQLite  │                                    │  │
│  │  └──────────┘                                    │  │
│  └──────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

### 5.2 核心数据表结构

#### 5.2.1 Executions表（Cassandra）

```sql
CREATE TABLE executions (
    namespace_id uuid,
    workflow_id text,
    run_id uuid,
    shard_id int,
    
    -- Mutable State
    execution blob,
    
    -- History Events
    history_tree blob,
    history_node blob,
    
    PRIMARY KEY ((shard_id), namespace_id, workflow_id, run_id)
) WITH CLUSTERING ORDER BY (namespace_id ASC, workflow_id ASC, run_id ASC);
```

#### 5.2.2 Tasks表

```sql
CREATE TABLE tasks (
    namespace_id uuid,
    task_queue_name text,
    task_queue_type int,
    task_id bigint,
    
    -- Task Data
    task blob,
    
    PRIMARY KEY ((namespace_id, task_queue_name, task_queue_type), task_id)
);
```

### 5.3 状态持久化流程

#### 5.3.1 创建Workflow Execution

```go
func (c *Context) CreateWorkflowExecution(
    ctx context.Context,
    shardContext shard.Context,
    createMode persistence.CreateWorkflowMode,
    prevRunID string,
    prevLastWriteVersion int64,
    newMutableState MutableState,
    newWorkflow *persistence.WorkflowSnapshot,
    newWorkflowEvents []*persistence.WorkflowEvents,
) error {
    // 1. 持久化Workflow Snapshot
    // 2. 持久化History Events
    // 3. 创建Transfer Tasks
    // 4. 更新Shard Ack Level
}
```

#### 5.3.2 更新Workflow Execution

```go
func (c *Context) UpdateWorkflowExecutionAsActive(
    ctx context.Context,
    shardContext shard.Context,
) error {
    // 1. 获取当前Mutable State
    // 2. 应用状态变更
    // 3. 持久化新的History Events
    // 4. 更新Mutable State
    // 5. 创建新的Tasks（Transfer/Timer）
    // 6. 更新Shard Ack Level
}
```

### 5.4 事务保证

**一致性保证机制**：

1. **Mutable State与History Events一致性**
   - 通过数据库事务保证
   - Mutable State存储最新Event ID

2. **Tasks与Mutable State一致性**
   - 使用Transactional Outbox模式
   - Tasks与Mutable State在同一事务中持久化

3. **跨服务一致性（History ↔ Matching）**
   - Transfer Queue确保任务最终送达
   - 幂等性设计防止重复处理

---

## 6. 核心流程实现机制

### 6.1 Workflow启动流程

```
┌──────────┐  StartWorkflowExecution  ┌──────────┐
│  Client  │ ─────────────────────────►│ Frontend │
└──────────┘                           └──────────┘
     │                                       │
     │                                       │ Route to History Shard
     │                                       ▼
     │                                 ┌──────────┐
     │                                 │  History │
     │                                 │  Shard   │
     │                                 └──────────┘
     │                                       │
     │                                       │ 1. Create Workflow Execution
     │                                       │    - Initialize Mutable State
     │                                       │    - Create History Events:
     │                                       │      * WorkflowExecutionStarted
     │                                       │      * WorkflowTaskScheduled
     │                                       │
     │                                       │ 2. Persist to Database
     │                                       │    - Write Mutable State
     │                                       │    - Write History Events
     │                                       │    - Create Transfer Task
     │                                       │
     │                                       │ 3. Queue Processor
     │                                       │    - Read Transfer Task
     │                                       │    - Call Matching.AddWorkflowTask
     │                                       ▼
     │                                 ┌──────────┐
     │                                 │ Matching │
     │                                 │ Service  │
     │                                 └──────────┘
     │                                       │
     │                                       │ Add Task to Task Queue
     │                                       │
     │                                       ▼
     │                                 ┌──────────┐
     │                                 │  Worker  │
     │                                 │ (Polling)│
     │                                 └──────────┘
```

### 6.2 Workflow Task执行流程

```
┌──────────┐  PollWorkflowTask  ┌──────────┐
│  Worker  │ ──────────────────►│ Matching │
└──────────┘                    │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 1. Match with Task
     │                                │    - Check taskC channel
     │                                │    - Return matched task
     │                                │
     │                                │ 2. Record Task Started
     │                                │    - Call History.RecordWorkflowTaskStarted
     │                                ▼
     │                          ┌──────────┐
     │                          │  History │
     │                          │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 3. Update Mutable State
     │                                │    - Add WorkflowTaskStarted event
     │                                │    - Create Timer Task (timeout)
     │                                │
     │                                │ 4. Return History Events
     │                                ▼
     │                          ┌──────────┐
     │                          │ Database │
     │                          └──────────┘
     │                                │
     │     WorkflowTask               │
     │ ◄──────────────────────────────┤
     │   (with History Events)        │
     │                                │
     │ 5. Execute Workflow Code       │
     │    - Replay History Events     │
     │    - Execute Workflow Logic    │
     │    - Generate Commands         │
     │                                │
     │ 6. RespondWorkflowTaskCompleted│
     │ ───────────────────────────────►
     │   Commands:                    │
     │   - ScheduleActivityTask       │
     │   - StartTimer                 │
     │   - CompleteWorkflow           │
     │                                │
     │                                ▼
     │                          ┌──────────┐
     │                          │  History │
     │                          │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 7. Process Commands
     │                                │    - Add History Events
     │                                │    - Update Mutable State
     │                                │    - Create Transfer Tasks
     │                                │
     │                                │ 8. Queue Processor
     │                                │    - Process Transfer Tasks
     │                                │    - Send tasks to Matching
     │                                ▼
```

### 6.3 Activity Task执行流程

```
┌──────────┐  PollActivityTask  ┌──────────┐
│  Worker  │ ──────────────────►│ Matching │
└──────────┘                    │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 1. Match with Task
     │                                │
     │                                │ 2. Record Task Started
     │                                ▼
     │                          ┌──────────┐
     │                          │  History │
     │                          │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 3. Update Mutable State
     │                                │    - Add ActivityTaskStarted event
     │                                │    - Update Activity Info
     │                                │    - Create Timer Tasks
     │                                │
     │     ActivityTask               │
     │ ◄──────────────────────────────┤
     │   (with Input)                 │
     │                                │
     │ 4. Execute Activity Code       │
     │                                │
     │ 5. RespondActivityTaskCompleted│
     │ ───────────────────────────────►
     │   (Result)                     │
     │                                │
     │                                ▼
     │                          ┌──────────┐
     │                          │  History │
     │                          │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ 6. Process Result
     │                                │    - Add ActivityTaskCompleted event
     │                                │    - Update Mutable State
     │                                │    - Schedule WorkflowTask
     │                                ▼
```

### 6.4 Timer处理流程

```
┌──────────┐                    ┌──────────┐
│  History │                    │  Timer   │
│ Service  │                    │  Queue   │
└──────────┘                    │Processor │
     │                          └──────────┘
     │                                │
     │ 1. Schedule Timer              │
     │    - Add TimerStarted event    │
     │    - Create Timer Task         │
     │                                │
     │                                │ 2. Wait for Timer Expiry
     │                                │    - Read Timer Tasks
     │                                │    - Check visibility time
     │                                │
     │                                │ 3. Execute Timer Task
     │                                ▼
     │                          ┌──────────┐
     │                          │  Timer   │
     │                          │ Executor │
     │                          └──────────┘
     │                                │
     │                                │ 4. Handle Timer Expiry
     │                                │    - Add TimerFired event
     │                                │    - Update Mutable State
     │                                │    - Schedule WorkflowTask
     │                                ▼
     │                          ┌──────────┐
     │                          │ Matching │
     │                          │ Service  │
     │                          └──────────┘
     │                                │
     │                                │ Add WorkflowTask
     │                                │
     │                                ▼
```

### 6.5 状态转换机制

**文件**: [service/history/api/update_workflow_util.go](file:///Users/bytedance/code/temporal/service/history/api/update_workflow_util.go)

所有状态转换使用统一的代码路径：

```go
func GetAndUpdateWorkflowWithNew(
    ctx context.Context,
    shardContext shard.Context,
    namespaceID namespace.ID,
    workflowID string,
    updateWorkflow func(workflowContext workflow.Context, mutableState workflow.MutableState) error,
) error {
    // 1. 获取Workflow Context
    workflowContext, err := shardContext.GetWorkflowContext(namespaceID, workflowID)
    
    // 2. 加载Mutable State
    mutableState, err := workflowContext.LoadMutableState(ctx, shardContext)
    
    // 3. 加锁
    if err := workflowContext.Lock(ctx, locks.PriorityHigh); err != nil {
        return err
    }
    defer workflowContext.Unlock()
    
    // 4. 执行状态更新
    if err := updateWorkflow(workflowContext, mutableState); err != nil {
        return err
    }
    
    // 5. 持久化状态
    if err := workflowContext.UpdateWorkflowExecutionAsActive(ctx, shardContext); err != nil {
        return err
    }
    
    return nil
}
```

**状态转换触发源**：
1. RPC from User Application (Start/Signal/Cancel/Terminate)
2. RPC from Worker (RespondWorkflowTaskCompleted/RespondActivityTaskCompleted)
3. Timer fired
4. Child Workflow completion

---

## 7. 性能优化与设计模式

### 7.1 关键优化技术

#### 7.1.1 同步匹配（Sync Match）

Matching Service优先尝试同步匹配，减少持久化开销：

```go
// 生产者：直接发送到channel
select {
case tm.taskC <- task:
    // 任务被poller立即接收，无需持久化
    return true, nil
default:
    // 无poller等待，需要持久化
}
```

#### 7.1.2 批量处理

History Service使用批量处理减少数据库写入：

```go
// 批量持久化History Events
func (c *Context) PersistWorkflowEvents(
    ctx context.Context,
    shardContext shard.Context,
    workflowEventsSlice ...*persistence.WorkflowEvents,
) (int64, error) {
    // 批量写入多个事件
}
```

#### 7.1.3 缓存机制

- **Mutable State缓存**: 减少数据库读取
- **History Events缓存**: 加速Workflow Task处理
- **Namespace缓存**: 减少元数据查询

### 7.2 设计模式

#### 7.2.1 Event Sourcing

所有状态变更通过事件记录：

```
Command → Event → Mutable State Update
```

#### 7.2.2 Transactional Outbox

确保任务创建与状态更新的一致性：

```
1. Update Mutable State (in transaction)
2. Create Transfer Task (in same transaction)
3. Queue Processor reads and processes tasks
```

#### 7.2.3 Sharding

通过分片实现水平扩展：

```
Workflow Execution → Shard ID → History Service Instance
```

---

## 8. 总结

### 8.1 核心设计原则

1. **持久化执行**: 所有状态变更持久化，支持故障恢复
2. **事件溯源**: 通过History Events重建Workflow状态
3. **分片扩展**: 通过Sharding支持大规模并发
4. **异步处理**: 通过Queue Processor解耦服务间依赖
5. **幂等性设计**: 所有操作支持重试，保证最终一致性

### 8.2 关键技术点

| 技术点 | 实现位置 | 核心价值 |
|--------|---------|---------|
| **Mutable State** | `service/history/workflow/` | Workflow状态管理核心 |
| **Task Matcher** | `service/matching/matcher.go` | 高效任务分发 |
| **Queue Processor** | `service/history/queues/` | 异步任务处理 |
| **Shard Engine** | `service/history/shard/` | 分片管理与路由 |
| **Worker Components** | `service/worker/` | 系统级任务执行 |

### 8.3 源码阅读建议

**推荐阅读顺序**：

1. **架构理解**: `docs/architecture/README.md`
2. **数据结构**: `service/history/workflow/mutable_state.go`
3. **核心服务**: `service/history/shard/engine.go`, `service/matching/matcher.go`
4. **任务处理**: `service/history/queues/`, `service/matching/task.go`
5. **持久化**: `common/persistence/`
6. **Worker实现**: `service/worker/`

**关键函数**：

| 函数 | 文件 | 功能 |
|------|------|------|
| `Engine.StartWorkflowExecution` | `service/history/shard/engine.go` | Workflow启动入口 |
| `TaskMatcher.Offer` | `service/matching/matcher.go` | 任务匹配核心逻辑 |
| `MutableState.AddHistoryEvent` | `service/history/workflow/mutable_state.go` | 事件添加 |
| `Context.UpdateWorkflowExecutionAsActive` | `service/history/workflow/context.go` | 状态持久化 |
| `workerManager.Start` | `service/worker/worker.go` | Worker启动 |

---

## 附录

### A. 关键配置参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `numHistoryShards` | 4 | History分片数量 |
| `numTaskQueuePartitions` | 4 | Task Queue分区数量 |
| `defaultTaskDispatchRPS` | 100000 | 默认任务分发速率 |
| `workflowTaskTimeout` | 10s | Workflow Task超时时间 |

### B. 监控指标

| 指标 | 类型 | 说明 |
|------|------|------|
| `task_dispatch_latency` | Histogram | 任务分发延迟 |
| `workflow_task_latency` | Histogram | Workflow Task处理延迟 |
| `persistence_latency` | Histogram | 持久化延迟 |
| `shard_count` | Gauge | 活跃Shard数量 |

### C. 故障排查

**常见问题**：

1. **Worker无法获取任务**
   - 检查Task Queue名称是否正确
   - 检查Worker是否已注册
   - 检查Matching Service日志

2. **Workflow执行卡住**
   - 检查History Service日志
   - 检查Mutable State状态
   - 检查Timer Task是否正常处理

3. **任务丢失**
   - 检查Transfer Queue处理日志
   - 检查Matching Service持久化
   - 检查数据库连接状态

