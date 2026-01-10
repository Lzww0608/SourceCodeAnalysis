# Hertz 架构设计深度解析

Hertz 是字节跳动开源的高性能 HTTP 框架，其设计充分借鉴了 Gin 和 Echo 等主流框架的优势，同时结合 Netpoll 实现了极致的性能优化。本文深入分析 Hertz 的架构设计思想。

## 🎯 设计目标

1. **高易用性**：参考 Gin/Echo 的 API 设计，降低学习成本
2. **高性能**：默认集成 Netpoll，支持标准库切换
3. **高扩展性**：分层设计，支持自定义网络库、协议等
4. **生产就绪**：完善的中间件、监控、日志等生态

---

## 📐 整体架构

### 分层架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Application Layer                         │
│              (用户业务代码、Handler、Middleware)              │
└────────────────────┬────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────┐
│                   Framework Core Layer                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐   │
│  │  Routing  │  │Middleware │  │ Protocol  │   │
│  │   System  │  │  Chain    │  │  Parser   │   │
│  └──────────┘  └──────────┘  └──────────┘   │
│  ┌──────────┐                                         │
│  │   Core    │  Context/Request/Response            │
│  └──────────┘                                         │
└────────────────────┬────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────┐
│                  Network Layer                                │
│  ┌──────────┐  ┌──────────┐                          │
│  │  Netpoll   │  │ Standard   │                          │
│  │  Adapter  │  │ Adapter   │                          │
│  └──────────┘  └──────────┘                          │
└────────────────────┬────────────────────────────────────────────┘
                     │
                     ▼
┌─────────────────────────────────────────────────────────────────────┐
│                 Transport Layer (OS Kernel)                   │
│              TCP Socket / epoll / HTTP Parser                │
└─────────────────────────────────────────────────────────────────────┘
```

### 目录结构

```
hertz/
├── pkg/
│   ├── app/                    # 应用层
│   │   ├── server/           # 服务端核心
│   │   │   ├── server.go   # 主 Server 实现
│   │   │   ├── binding.go   # 数据绑定
│   │   │   ├── render.go    # 渲染引擎
│   │   │   ├── option.go    # 配置选项
│   │   │   └── registry/   # 服务注册
│   │   ├── context.go         # Context 实现
│   │   └── middlewares/     # 中间件
│   ├── route/                 # 路由系统
│   │   ├── tree.go           # Radix Tree 路由树
│   │   ├── routergroup.go   # 路由组
│   │   └── param.go         # 路径参数
│   ├── network/               # 网络层
│   │   ├── netpoll/         # Netpoll 适配
│   │   ├── standard/         # 标准库适配
│   │   └── transport.go     # Transport 接口
│   └── protocol/             # 协议层
│       ├── http1/           # HTTP/1.1 实现
│       │   └── ...
│       └── http2/           # HTTP/2 实现（扩展）
└── ...
```

---

## 🔑 核心接口设计

### 1. Handler 接口

```go
// Handler 是用户处理请求的入口
type Handler interface {
	ServeHTTP(ctx context.Context, ctx *RequestContext)
}
```

**设计要点**：
- **简洁性**：只有一个方法，降低使用门槛
- **Context 参数**：第一个参数是标准 `context.Context`，支持超时和取消
- **RequestContext 参数**：第二个参数是 Hertz 封装的请求上下文

**使用示例**：
```go
func MyHandler(ctx context.Context, c *app.RequestContext) {
    // 1. 获取请求数据
    path := c.Request.Path()
    method := c.Request.Method()
    
    // 2. 获取参数
    id := c.Param("id")
    query := c.Query("name")
    
    // 3. 设置响应
    c.JSON(map[string]interface{}{
        "path":  path,
        "method": method,
        "id":     id,
        "name":   query,
    })
}
```

### 2. Engine 接口

```go
// Engine 是 Hertz 的核心引擎
type Engine struct {
    // 路由相关
    router router.Router
    
    // 中间件
    middleware middleware.MiddlewareChain
    
    // 网络层
    network network.Network
    
    // 协议相关
    protocol      protocol.Protocol
    protocolCodec protocol.Codec
    
    // 渲染引擎
    render render.Render
    
    // 配置选项
    options *options.Options
    
    // ClientIP 获取函数
    getClientIP func(ctx *RequestContext) string
    
    // ...
}
```

**核心职责**：
1. **路由管理**：注册、查找路由
2. **中间件编排**：构建中间件链
3. **网络适配**：管理 Netpoll/标准库的切换
4. **协议解析**：HTTP 请求的解析和响应构造
5. **生命周期管理**：启动、优雅关闭

---

## 🌐 路由系统架构

### Radix Tree 路由

Hertz 使用压缩前缀树（Radix Tree 或 Prefix Tree）实现路由查找。

```go
// 路由树节点类型
type nodeType uint8

const (
    staticRoot nodeType = 1 << iota // 静态路由根
    root                            // 动态路由根
    param                            // 带参数路由
    catchAll                          // 通配符路由
)
```

#### 路由树结构

```
示例路由注册：
GET    /user/profile
GET    /user/:id
GET    /user/:id/posts
POST   /user/:id/posts
GET    /user/*/info

Radix Tree 结构：
                    [root]
                       |
          ┌────────────┴────────────┐
          |                      |                      |
       [GET]                  [POST]              [DELETE]
          |                      |                      |
    [user]                 [user]                [user]
       |                      |                      |
  ┌────┴────┐            ┌────┴────┐            ...
  |         |            |         |
[profile] [ :id ]    [ :id ]  [ :id ]
  |         |            |         |
  []       [posts]      [posts]    ...
```

#### 路由查找流程

```go
func findRoute(method string, path string) *router.Route {
    // 1. 根据 HTTP Method 跳转到对应的根节点
    root := methodRoots[method]
    if root == nil {
        return nil
    }
    
    // 2. 遍历 Radix Tree
    node := root
    for i := 0; i < len(path); {
        // 3. 跳过相同的路径前缀
        if i < node.prefixLen {
            i++
            continue
        }
        
        // 4. 匹配子节点
        char := path[i]
        child := node.children[char]
        if child == nil {
            // 没有匹配的路由
            return nil
        }
        
        node = child
    }
    
    // 5. 检查节点类型
    switch node.kind {
    case staticRoot, root, param:
        // 完全匹配
        return node.route
    case catchAll:
        // 通配符匹配
        return node.route
    default:
        // 部分匹配，继续处理
        return nil
    }
}
```

### 路由参数提取

Hertz 支持多种参数格式：

```go
// 参数类型
type paramKind uint8

const (
    paramKindPath    paramKind = 1 << iota // 路径参数 :id
    paramKindQuery                            // 查询参数 ?name=value
    paramKindForm                              // 表单参数 application/x-www-form-urlencoded
    paramKindPostForm                           // POST 表单 multipart/form-data
)
```

**参数提取示例**：
```go
// 路由: GET /user/:id/posts/:postid
// 请求: GET /user/123/posts/456

c.Param("id")      // "123"
c.Param("postid")   // "456"
c.Query("sort")    // 从 ?sort=new 提取
```

---

## 🧅 中间件系统架构

Hertz 采用洋葱圈模型实现中间件。

### 中间件定义

```go
// Middleware 是中间件的类型定义
type Middleware func(Handler) Handler
```

**设计要点**：
- **函数式设计**：中间件是一个高阶函数，接收 Handler 返回 Handler
- **洋葱模型**：外层包裹内层，层层传递
- **解耦性**：中间件不依赖于具体 Handler 实现

### 中间件链构建

```go
// 中间件链执行
func (h *Handler) ServeHTTP(ctx context.Context, c *RequestContext) {
    // 构建完整的处理链
    handler := h.middleware.chain(h.handler)
    
    // 执行中间件链
    handler(ctx, c)
}
```

### 中间件执行流程

```
请求 → Middleware A
          │
          ├─── Pre-process（前置逻辑）
          ▼
      Middleware B
          │
          ├─── Pre-process
          ▼
      Middleware C
          │
          ├─── Pre-process
          ▼
         Handler
          │
          ├─── Business Logic（业务逻辑）
          ▼
      Middleware C
          │
          └─── Post-process（后置逻辑）
          ▼
      Middleware B
          │
          └─── Post-process
          ▼
      Middleware A
          │
          └─── Post-process
          ▼
         响应
```

### 内置中间件

#### 1. Recovery 中间件

```go
// Panic 恢复中间件
func Recovery() app.HandlerFunc {
    return func(ctx context.Context, c *app.RequestContext) {
        defer func() {
            if r := recover(); r != nil {
                // 记录 Panic 信息
                h.log.Error("panic recovered",
                    zap.Any("error", r),
                    zap.String("path", c.Request.Path()),
                )
                
                // 返回 500 错误
                c.JSON(500, map[string]interface{}{
                    "error": "Internal Server Error",
                })
            }
        }()
        
        // 执行后续 Handler
        c.Next()
    }
}
```

#### 2. CORS 中间件

```go
// 跨域资源共享中间件
func CORS() app.HandlerFunc {
    return func(ctx context.Context, c *app.RequestContext) {
        c.Header("Access-Control-Allow-Origin", "*")
        c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
        c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
        c.Header("Access-Control-Max-Age", "86400")
        
        // 处理 OPTIONS 预检请求
        if c.Request.Method() == "OPTIONS" {
            c.Status(204)
            return
        }
        
        c.Next()
    }
}
```

---

## 🌊 Context 管理架构

Context 是 Hertz 请求处理上下文的核心。

### Context 接口

```go
// RequestContext 扩展标准 context.Context
type RequestContext interface {
    context.Context
    
    // 请求数据访问
    GetMethod() string
    GetPath() string
    GetParam(key string) string
    GetQuery(key string) string
    PostForm(key string) string
    
    // 响应数据设置
    Status(code int)
    Header(key, value string)
    JSON(obj interface{})
    String(s string)
    
    // 中间件控制
    Next()
    Abort()
    
    // ...
}
```

### Context 实现

```go
type Context struct {
    context.Context
    
    // 请求相关
    request protocol.Request
    response protocol.Response
    
    // 路由信息
    router router.Route
    
    // 中间件状态
    index     int8
    handlers  []Handler
    keys      map[string]interface{}
    
    // ...
}
```

### Context 生命周期

```
创建请求 ──► New Context
       │
       ├─► 绑定 Request 和 Response
       ├─► 设置初始路由信息
       └─► 初始化中间件索引
       │
       ▼
执行中间件链 ──► 执行 Middleware 1 ... N
       │
       ├─► Next() 调用下一个中间件
       └─► Abort() 中止执行
       │
       ▼
执行 Handler ──► 调用用户 Handler
       │
       ├─► 业务逻辑处理
       └─► 设置响应数据
       │
       ▼
销毁 Context ──► 清理资源
       │
       ├─► 释放 Request/Response
       ├─► 清理 keys
       └─► 回收到对象池
       │
       ▼
回收 ──► 归还到对象池
```

---

## 🕸 网络层架构

Hertz 通过抽象的网络层接口，支持 Netpoll 和标准库的切换。

### Transport 接口

```go
// Transport 是网络传输层的抽象
type Transport interface {
    // 监听地址
    ListenAndServe(addr string, handler network.Handler) error
    
    // 关闭监听
    Close() error
    
    // 连接选项
    Dialer() network.Dialer
}
```

### Netpoll 适配

```go
// Netpoll Transport 实现
type netpollTransport struct {
    // Netpoll 配置
    options []netpoll.Option
    
    // ...
}

func (t *netpollTransport) ListenAndServe(addr string, handler network.Handler) error {
    // 1. 创建 Netpoll Listener
    listener, err := netpoll.CreateListener(
        "tcp", 
        addr, 
        t.options...,
    )
    if err != nil {
        return err
    }
    
    // 2. 设置事件回调
    listener.SetOnRequest(func(ctx context.Context, conn netpoll.Connection) error {
        // 3. 适配 Netpoll Connection 到 Hertz Request
        req := adaptRequest(conn)
        resp := adaptResponse(conn)
        
        // 4. 调用 Hertz Handler
        handler(ctx, req, resp)
        
        return nil
    })
    
    // 5. 开始监听
    return listener.Run()
}
```

**关键适配点**：
1. **Connection → Request**：将 Netpoll 的 Reader/Writer 适配到 Hertz 的 Request 对象
2. **Response → Connection**：将 Hertz 的 Response 对象适配到 Netpoll 的 Writer 接口
3. **事件驱动**：将 Netpoll 的 OnRequest 回调桥接到 Hertz 的 Handler 调用

### 标准库适配

```go
// 标准 Transport 实现
type standardTransport struct {
    // 标准库配置
    server *http.Server
    handler http.Handler
}

func (t *standardTransport) ListenAndServe(addr string, handler network.Handler) error {
    // 1. 创建标准 Server
    t.server = &http.Server{
        Addr:    addr,
        Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // 2. 适配标准 Request/Response 到 Hertz
            req := adaptStandardRequest(r)
            resp := adaptStandardResponse(w)
            
            // 3. 调用 Hertz Handler
            handler(req.Context(), req, resp)
        }),
    }
    
    // 4. 开始监听
    return t.server.ListenAndServe()
}
```

**优势**：
- **兼容性好**：完全兼容 Go 标准库的接口
- **开发友好**：可以直接使用标准库的调试工具
- **简单场景**：低并发场景下性能足够

---

## 📄 协议层架构

Hertz 通过抽象的协议层，支持 HTTP/1.1、HTTP/2 和自定义协议。

### Protocol 接口

```go
// Protocol 是协议层的抽象
type Protocol interface {
    // 解析请求
    Parse(c protocol.Conn) protocol.Request
    
    // 编码响应
    Codec() protocol.Codec
}
```

### HTTP/1.1 解析

```go
// HTTP/1.1 请求解析
func parseHTTPRequest(conn protocol.Conn) protocol.Request {
    // 1. 读取请求行
    line, _ := conn.Reader().ReadString('\n')
    method, path, version := parseRequestLine(line)
    
    // 2. 读取请求头
    headers := readHeaders(conn.Reader())
    
    // 3. 读取请求体
    var body io.Reader
    if contentLength := headers.Get("Content-Length"); contentLength != "" {
        body = io.LimitReader(conn.Reader(), parseLength(contentLength))
    } else if transferEncoding := headers.Get("Transfer-Encoding"); transferEncoding == "chunked" {
        body = parseChunked(conn.Reader())
    }
    
    return protocol.Request{
        Method:  method,
        Path:    path,
        Version:  version,
        Header:  headers,
        Body:    body,
    }
}
```

### 响应构造

```go
// HTTP 响应构造
func (r *response) build() []byte {
    var buf []byte
    
    // 1. 状态行
    buf = append(buf, []byte(fmt.Sprintf("HTTP/1.1 %d %s\r\n", 
        r.status, r.statusText))...)
    
    // 2. 响应头
    for k, v := range r.header {
        buf = append(buf, []byte(fmt.Sprintf("%s: %s\r\n", k, v))...)
    }
    
    // 3. 空行
    buf = append(buf, []byte("\r\n")...)
    
    // 4. 响应体
    buf = append(buf, r.body...)
    
    return buf
}
```

---

## 🎨 渲染引擎架构

Hertz 支持多种渲染方式。

### Render 接口

```go
// Render 是渲染引擎的抽象
type Render interface {
    // JSON 渲染
    JSON(c context.Context, obj interface{})
    
    // XML 渲染
    XML(c context.Context, obj interface{})
    
    // HTML 渲染
    HTML(c context.Context, name string, obj interface{})
    
    // String 渲染
    String(c context.Context, format string, values ...interface{})
}
```

### 内置渲染实现

#### JSON 渲染

```go
func (r *jsonRender) JSON(c context.Context, obj interface{}) {
    c.SetContentType("application/json; charset=utf-8")
    c.SetStatusCode(200)
    
    // 使用 Sonic 高性能 JSON 序列化
    data, _ := sonic.Marshal(obj)
    c.Response.SetBodyRaw(data)
    c.Response.Header().Set("Content-Length", strconv.Itoa(len(data)))
}
```

#### HTML 渲染

```go
func (r *htmlRender) HTML(c context.Context, name string, obj interface{}) {
    // 从模板引擎加载模板
    tpl, err := r.engine.LoadTemplate(name)
    if err != nil {
        c.Error(500, err.Error())
        return
    }
    
    // 执行模板渲染
    c.SetContentType("text/html; charset=utf-8")
    c.SetStatusCode(200)
    c.Response.SetBodyRaw(tpl.Execute(obj))
}
```

---

## 🔧 配置系统架构

Hertz 提供了丰富的配置选项。

### Options 结构

```go
type Options struct {
    // 网络配置
    Network string              // "tcp" / "unix"
    Addr     string              // 监听地址
    
    // 性能配置
    ReadBufferSize  int         // 读缓冲区大小
    WriteBufferSize int         // 写缓冲区大小
    MaxBodySize    int         // 最大请求体大小
    
    // 日志配置
    Logger interface{}          // 日志接口
    
    // 路由配置
    DisablePrintRoute bool      // 禁用路由打印
    DisableRouteColors bool    // 禁用路由颜色
    
    // 中间件配置
    UseRawPath bool            // 使用原始路径
    
    // 其他配置
    ClientIP      ClientIPOptions
    StreamBody    bool
    // ...
}
```

### Server 启动

```go
func New(opts ...Option) *Engine {
    // 1. 合并选项
    options := defaultOptions()
    for _, opt := range opts {
        opt(&options)
    }
    
    // 2. 初始化 Engine
    e := &Engine{
        router:      router.NewRouter(),
        middleware:  middleware.NewChain(),
        network:     network.New(options.Network),
        protocol:    protocol.NewHTTP1(),
        // ...
    }
    
    // 3. 应用选项
    e.applyOptions(options)
    
    return e
}
```

---

## 🚀 启动流程

### Server 初始化

```
New(opts...) ──► Engine 初始化
       │
       ├─► 创建 Router（Radix Tree）
       ├─► 创建 Middleware Chain
       ├─► 初始化 Network Layer
       ├─► 初始化 Protocol Layer
       ├─► 初始化 Render Engine
       └─► 应用配置选项
       │
       ▼
ListenAndServe ──► 启动监听
       │
       ├─► 根据网络类型选择 Transport
       ├─► Netpoll: 创建 Netpoll Listener
       └─► Standard: 创建标准 Server
       │
       ▼
       ├─► 设置 OnRequest 回调
       ├─► 注册路由
       └─► 开始事件循环
```

### 请求处理流程

```
连接建立 ──► 新连接到达
       │
       ▼
解析请求 ──► Protocol.Parse()
       │
       ├─► 解析请求行
       ├─► 解析请求头
       ├─► 解析请求体
       └─► 构建 Request 对象
       │
       ▼
路由查找 ──► Router.Find()
       │
       ├─► Radix Tree 查找
       ├─► 参数提取
       └─► 路由匹配
       │
       ▼
中间件链 ──► 执行中间件
       │
       ├─► Middleware 1 Pre-process
       ├─► Middleware 2 Pre-process
       └─► ... 
       │
       ▼
执行 Handler ──► Handler.ServeHTTP()
       │
       ├─► 业务逻辑处理
       ├─► 获取数据
       └─► 构造响应
       │
       ▼
返回响应 ──► 响应写入
       │
       ├─► 序列化响应体
       ├─► 设置响应头
       └─► 写入网络层
       │
       ▼
连接关闭 ──► 资源清理
       │
       ├─► Context 回收
       └─► Connection 关闭
```

---

## 📊 架构设计亮点

### 1. 分层解耦

每一层都有清晰的职责：
- **应用层**：业务逻辑
- **路由层**：URL 分发
- **中间件层**：横切关注点
- **协议层**：协议解析
- **网络层**：I/O 处理

### 2. 接口抽象

通过接口抽象实现可替换性：
- **Transport 接口**：Netpoll ↔ 标准库切换
- **Protocol 接口**：HTTP/1.1 ↔ HTTP/2 ↔ 自定义协议
- **Render 接口**：JSON/XML/HTML 渲染器切换

### 3. 依赖注入

- **Option 模式**：通过函数选项配置
- **接口注入**：Network、Protocol、Render 可自定义
- **控制反转**：Hertz 管理依赖，用户只需提供接口

### 4. 高性能设计

- **Radix Tree**：O(K) 路由查找
- **对象池**：Context、Request、Response 复用
- **零拷贝**：Netpoll 的 I/O 优化
- **协程池**：中间件执行使用协程池

### 5. 易用性设计

- **简洁 API**：参考 Gin/Echo 的设计
- **丰富文档**：详细的用户指南和 API 文档
- **错误处理**：统一的错误处理和 Recovery 机制
- **开发体验**：热重载、调试模式等

---

## 🔍 与其他框架对比

| 特性 | Hertz | Gin | Echo |
|--------|--------|-----|------|
| 路由算法 | Radix Tree | Radix Tree | Radix Tree |
| 网络库 | Netpoll + 标准库 | 标准库 | 标准库 |
| 中间件模型 | 洋葱模型 | 洋葱模型 | 洋葱模型 |
| 性能 | 极高 | 高 | 高 |
| 可扩展性 | 分层设计 | 插件设计 | 中间件设计 |
| 学习成本 | 低 | 低 | 低 |

---

## 📚 总结

Hertz 的架构设计体现了以下工程实践：

1. **SOLID 原则**：单一职责、开放封闭、依赖倒置
2. **高性能优先**：Netpoll 集成、Radix Tree、对象池
3. **可扩展性**：接口抽象、插件化、依赖注入
4. **用户体验**：简洁 API、丰富文档、错误友好
5. **生产就绪**：完善的中间件、监控、日志生态

理解 Hertz 的架构设计，是深入学习高性能 HTTP 框架设计的关键。

