# Hertz HTTP 框架源码分析

本目录包含了 Hertz HTTP 框架的深度源码分析。Hertz 是字节跳动开源的高性能、高易用性 Golang HTTP 框架，默认集成 Netpoll，支持标准库切换。

## 🎯 分析目标

1. **架构理解**：掌握 Hertz 的分层设计和组件交互
2. **核心机制**：深入理解路由、中间件、协议解析等核心机制
3. **性能优化**：学习 Hertz 如何通过 Netpoll 实现高性能
4. **可扩展性**：理解 Hertz 的插件化设计和扩展机制
5. **工程实践**：学习字节跳动的工程化实践和代码组织

---

## 📐 分析进度

这是一个动态更新的列表，用于跟踪各个模块的分析进度。

### 整体架构

- [x] **架构设计** - Hertz 的分层架构和接口设计
- [x] **学习计划** - 系统化的学习路径和阶段规划

### 核心组件

* [ ] **Application Layer** (应用层)
    * [ ] Server 实现 (pkg/app/server)
    * [ ] Context 实现 (pkg/app/context)
    * [ ] Request/Response 对象
    * [ ] 中间件系统 (pkg/app/middlewares)

* [ ] **Routing System** (路由系统)
    * [ ] Radix Tree 路由树 (pkg/route/tree)
    * [ ] 路由匹配算法 (pkg/route/param)
    * [ ] 路由组管理 (pkg/route/routergroup)

* [ ] **Network Layer** (网络层)
    * [ ] Transport 接口 (pkg/network/transport)
    * [ ] Netpoll 适配 (pkg/network/netpoll)
    * [ ] 标准库适配 (pkg/network/standard)
    * [ ] 协议扩展 (pkg/network/protocol)

* [ ] **Protocol Layer** (协议层)
    * [ ] HTTP/1.1 实现
    * [ ] 请求解析 (pkg/protocol/request)
    * [ ] 响应构造 (pkg/protocol/response)
    * [ ] 协议扩展 (HTTP/2/WebSocket)

* [ ] **Middleware System** (中间件系统)
    * [ ] 中间件链构建
    * [ ] 内置中间件 (Recovery/CORS 等)
    * [ ] Context 传递机制

* [ ] **Performance** (性能优化)
    * [ ] 性能测试结果分析
    * [ ] 内存优化技巧
    * [ ] 并发优化策略

---

## 📂 分析文档

### 总览文档

- ✅ **plan.md** - 系统化的学习计划（30 天分阶段）
- ✅ **architecture.md** - 完整的架构设计解析

### 核心组件文档

#### Application Layer (应用层)

待创建：
- **server.md** - Server 启动和管理机制
- **context.md** - Context 实现和生命周期
- **request_response.md** - Request/Response 对象设计

#### Routing System (路由系统)

待创建：
- **tree.md** - Radix Tree 路由算法详解
- **matching.md** - 路由匹配逻辑和参数提取
- **group.md** - 路由组的管理和中间件继承

#### Network Layer (网络层)

待创建：
- **transport.md** - Transport 接口抽象
- **netpoll_adapter.md** - Netpoll 适配实现
- **standard_adapter.md** - 标准库适配实现

#### Protocol Layer (协议层)

待创建：
- **http11.md** - HTTP/1.1 协议解析实现
- **request.md** - HTTP 请求解析流程
- **response.md** - HTTP 响应构造流程
- **extension.md** - 协议扩展机制（HTTP/2/WebSocket）

#### Middleware System (中间件系统)

待创建：
- **chain.md** - 中间件链的构建和执行
- **built_in.md** - 内置中间件实现
- **best_practices.md** - 中间件开发最佳实践

#### Performance (性能优化)

待创建：
- **benchmark.md** - 性能测试结果分析
- **memory.md** - 内存优化和对象池使用
- **concurrency.md** - 并发控制和协程池
- **comparison.md** - 与其他框架的对比

---


---

## 🔗 相关资源

- [Hertz 官方仓库](https://github.com/cloudwego/hertz)
- [Hertz 官方文档](https://www.cloudwego.io/zh/docs/hertz/)
- [Hertz Examples](https://github.com/cloudwego/hertz-examples)
- [Hertz RoadMap](https://github.com/cloudwego/hertz/blob/main/ROADMAP.md)

---

**加油！通过系统化的学习，深度掌握 Hertz 框架的设计思想！💪**

