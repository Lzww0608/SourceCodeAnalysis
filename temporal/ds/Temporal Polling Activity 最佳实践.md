# Temporal Polling Activity 最佳实践

## 推荐模式

| 场景 | 推荐方案 |
|------|----------|
| 简单轮询 | 使用 Activity 重试机制（低频轮询需要考虑日志级别，以抑制频繁的日志输出） |
| 非常频繁的轮询 | 在 Activity 中将轮询实现为一个循环（注意设置合适的 HeartBeat 以支持断点续传） |
| 需要周期性执行一系列 Activity 或者 Activity 参数在重试间需要变化 | 使用子工作流 + 定期 continue-as-new |

## 历史大小影响

子工作流只会给父工作流历史添加 **3 个事件**：

- `StartChildWorkflowExecutionInitiated`
- `ChildWorkflowExecutionStarted`
- `ChildWorkflowExecutionCompleted/Failed/Cancelled`

**结论**：对于低频轮询（如每小时一次），子工作流对父工作流历史影响很小

## 实践场景分析

### 场景一

#### 问题背景

**场景描述**：
- 工作流在等待异步下游操作完成时暂停
- 下游系统通过 Kafka 发布事件，服务监听后通过 webhook 发送信号恢复 Temporal 工作流
- **痛点**：外部事件可能丢失或未送达，导致工作流无限期暂停并最终超时

**用户需求**：实现轮询回退机制，当工作流重置或重试时，轮询下游系统验证操作状态

#### 核心技术问题

**并发处理需求**：父工作流需要同时：
1. 启动子工作流定期轮询下游状态
2. 监听外部信号（可随时设置 `workflowPaused` 标志）

**问题**：`childWorkflow.exec(...)` 会阻塞父工作流，导致无法并发响应信号

#### 推荐解决方案

```java
ChildWorkflow child = Workflow.newChildWorkflowStub(ChildWorkflow.class, childWorkflowOptions);
Promise<String> result = Async.function(child::executeChild);
result.thenApply((String r) -> {
    done = true;
    return r;
});

Workflow.await(() -> done || signal_received);
```

```go
childFuture := workflow.ExecuteChildWorkflow(ctx, ChildWorkflow, childInput)

done := false
signalReceived := false

signalCh := workflow.GetSignalChannel(ctx, "signal_received")
selector := workflow.NewSelector(ctx)

selector.AddFuture(childFuture, func(f workflow.Future) {
    done = true
})
selector.AddReceive(signalCh, func(c workflow.ReceiveChannel, more bool) {
    c.Receive(ctx, nil)
    signalReceived = true
})

for !done && !signalReceived {
    selector.Select(ctx)
}
```

**关键点**：
- 使用 `Async.function()` 异步启动子工作流
- 使用 `Promise.thenApply()` 处理完成回调
- 使用 `Workflow.await()` 同时等待轮询完成或信号到达
- 可配合 `CancellationScope` 在信号先到达时取消子工作流
