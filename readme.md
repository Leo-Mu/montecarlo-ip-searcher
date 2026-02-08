# Monte Carlo IP Searcher（mcis）

一个 **Cloudflare IP 优选**工具：用蒙特卡洛搜索算法，在更少探测次数下，从 IPv4/IPv6 网段里找到更快、更稳定的 IP。

## 为什么选择 mcis？

**传统 IP 优选工具的两难困境：**
- **扫描少了** → 随机打靶，可能错过真正的好 IP
- **扫描多了** → 耗时太长，还容易触发运营商风控

**mcis 的核心价值：用 1/600 的探测次数，找到接近全段扫描的最优 IP。**

传统工具是"广撒网"，而 mcis 是"精准制导"——通过智能搜索把有限的探测预算集中到真正有潜力的 IP 段，好钢用在刀刃上。

**效率对比（以 Cloudflare IPv4 为例）：**

| 工具/模式 | 探测次数 | 说明 |
|-----------|----------|------|
| 全段暴力扫描 | ~180 万 | Cloudflare IPv4 约 180 万个 IP |
| CloudflareSpeedTest 默认 | ~1 万 | 每个 /24 段随机测 1 个 |
| **mcis 推荐配置** | **3000** | 智能搜索，聚焦有潜力区域 |

**mcis 如何做到的？**

| 特性 | 说明 |
|------|------|
| **递进式下钻** | 发现某个子网表现好，就继续细分探索这个区域，而不是把时间浪费在差的区域 |
| **多头分散探索** | 多个搜索头并行探索不同区域，通过"排斥力"机制避免都陷入同一个局部最优 |
| **贝叶斯优化** | 使用 Thompson Sampling 算法，自动平衡"探索新区域"和"利用已知好区域"，无需手动调参 |
| **多次测试取平均** | 每个 IP 默认测试 6 次，跳过首次握手开销，取平均值，结果更稳定准确 |

## 下载安装

[Release](https://github.com/Leo-Mu/montecarlo-ip-searcher/releases/latest) 下载解压后，在文件夹中右键打开终端即可运行。

## 推荐配置

**直接复制使用，无需调参：**

```bash
# IPv4 推荐配置
./mcis -v --out text --cidr-file ./ipv4cidr.txt --budget 3000 --concurrency 100

# IPv6 推荐配置
./mcis -v --out text --cidr-file ./ipv6cidr.txt --budget 4000 --heads 16 --concurrency 100
```

从源码运行：

```bash
# IPv4
go run ./cmd/mcis -v --out text --cidr-file ./ipv4cidr.txt --budget 3000 --concurrency 100

# IPv6
go run ./cmd/mcis -v --out text --cidr-file ./ipv6cidr.txt --budget 4000 --heads 16 --concurrency 100
```

**为什么 IPv6 配置不同？**
- IPv6 地址空间远大于 IPv4，需要更多探测次数（budget）才能收敛
- 更多搜索头（heads）可以并行探索更广的区域，避免陷入局部最优

**提示：** 推荐在晚高峰时段运行测试，因为此时不同 IP 之间的延迟差异更明显，算法更容易找到最优解。

## 参数速查表

### 常用参数

| 参数 | 默认值 | 推荐值 | 说明 |
|------|--------|--------|------|
| `--budget` | 2000 | IPv4: 3000, IPv6: 4000 | 总探测次数，越大结果越稳定 |
| `--concurrency` | 200 | 100 | 并发探测数 |
| `--heads` | 4 | IPv4: 4, IPv6: 16 | 搜索头数量，越多探索越广 |
| `--top` | 20 | 20 | 输出 Top N 个最优 IP |
| `--timeout` | 3s | 3s | 单次探测超时 |
| `-v` | 关闭 | 开启 | 显示搜索进度 |
| `--out` | jsonl | text | 输出格式：text/jsonl/csv |

### 高级参数（一般无需修改）

| 参数 | 默认值 | 取值范围 | 说明 |
|------|--------|----------|------|
| `--beam` | 32 | 16-64 | 每个搜索头保留的候选数 |
| `--diversity-weight` | 0.3 | 0-1 | 多样性权重，越高越分散探索 |
| `--split-interval` | 20 | 10-30 | 每 N 个样本检查一次拆分 |
| `--min-samples-split` | 5 | 3-10 | 前缀至少采样 N 次才允许拆分 |
| `--split-step-v4` | 2 | 1-8 | IPv4 下钻步长（如 /16→/18） |
| `--split-step-v6` | 4 | 1-16 | IPv6 下钻步长（如 /32→/36） |
| `--max-bits-v4` | 24 | 1-32 | IPv4 最大前缀长度 |
| `--max-bits-v6` | 56 | 1-128 | IPv6 最大前缀长度 |
| `--rounds` | 6 | 3-10 | 每个 IP 测试次数 |
| `--skip-first` | 1 | 0-3 | 跳过前 N 次测试（去除握手开销） |
| `--seed` | 0 | ≥0 | 随机种子（0=时间种子） |

## 参数详解

### 基础参数

**输入网段：**
- `--cidr`：直接指定 CIDR，可重复使用。例：`--cidr 1.1.1.0/24 --cidr 1.0.0.0/24`
- `--cidr-file`：从文件读取 CIDR，每行一个，支持 `#` 注释

**搜索控制：**
- `--budget`：总探测次数。**越大越稳定，但耗时越长**。IPv6 空间大，建议 4000+
- `--concurrency`：并发数。建议 50-200，过高可能导致网络拥塞
- `--top`：输出前 N 个最优 IP

**输出控制：**
- `--out`：输出格式
  - `text`：人类可读格式（推荐日常使用）
  - `jsonl`：JSON Lines 格式（适合程序解析）
  - `csv`：CSV 格式（适合导入表格）
- `--out-file`：输出到文件（默认输出到终端）
- `-v`：显示搜索进度（强烈推荐开启）

### 搜索算法参数

- `--heads`：搜索头数量。多个搜索头并行探索不同区域，通过"排斥力"机制避免都跑到同一个局部最优。IPv6 建议 8-16
- `--beam`：每个搜索头保留的候选前缀数。越大探索越发散
- `--diversity-weight`：多样性权重（0-1）。越高，搜索头之间越分散

### 探测配置

- `--host`：目标域名，同时设置 TLS SNI 和 HTTP Host header。默认 `example.com`
- `--path`：请求路径。默认 `/cdn-cgi/trace`（Cloudflare 标准端点）
- `--timeout`：单次探测超时。注意：实际超时 = timeout × rounds
- `--rounds`：每个 IP 测试次数。默认 6 次，取平均值减少波动
- `--skip-first`：跳过前 N 次测试。默认 1（跳过首次握手开销）

**提示：** 使用你自己的网站作为 `--host`，可以确保优选出的 IP 对你的网站生效：

```bash
./mcis -v --out text --cidr-file ./ipv4cidr.txt --host your-domain.com --budget 3000 --concurrency 100
```

## 可选功能

### CDN 节点过滤

根据 CDN 机房代码（colo）过滤结果：

- `--colo`：白名单，只保留指定机房。例：`--colo HKG,SJC`
- `--colo-exclude`：黑名单，排除指定机房。例：`--colo-exclude LAX,DFW`

两者只能二选一。

### 下载测速

对排名靠前的 IP 进行下载速度测试：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--download-top` | 5 | 对 Top N IP 测速（0=关闭） |
| `--download-bytes` | 50000000 | 下载大小（字节）；使用 `--download-url` 时不传则默认不限制 |
| `--download-timeout` | 45s | 单 IP 测速超时 |
| `--download-url` | （空） | 自定义测速文件地址（见下方说明） |

**自定义测速地址：** 由于 Cloudflare 默认测速端点 `speed.cloudflare.com/__down` 对生成的下载文件大小可能存在限制，可通过 `--download-url` 指定自定义的测速文件地址。

**指定 `--download-url` 时，默认不限制下载大小**：会下载完整文件直至 EOF，再按实际字节数与耗时计算速度。若需限制流量或时间，可加 `--download-bytes N`（最多读取 N 字节后停止）。未指定自定义 URL 时，仍使用默认 50MB 测速。

```bash
# 自定义地址：默认下载完整文件再算速度
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-url https://your-domain.com/path/to/largefile

# 自定义地址且限制只下载前 50MB
./mcis -v --out text --cidr-file ./ipv4cidr.txt --download-url https://your-domain.com/path/to/largefile --download-bytes 50000000
```

**注意：** 不限制大小时单次下载可能很大，请视情况调大 `--download-timeout`；流量约等于「文件大小 × 参与测速的 IP 数」。

**可用的测速大文件地址：** 可使用自己部署在 Cloudflare 后的静态大文件；或使用走 Cloudflare CDN 的公开下载链接（如厂商官网的安装包、镜像等）。社区整理的可选地址可参考 [CloudflareSpeedTest 讨论区](https://github.com/XIU2/CloudflareSpeedTest/discussions/490)。

### DNS 自动上传

搜索完成后，自动将优选 IP 上传到 DNS 服务商。支持 **Cloudflare** 和 **Vercel**。

| 参数 | 说明 |
|------|------|
| `--dns-provider` | DNS 服务商：`cloudflare` 或 `vercel` |
| `--dns-token` | API Token（或用环境变量 `CF_API_TOKEN` / `VERCEL_TOKEN`） |
| `--dns-zone` | Zone ID（Cloudflare）或域名（Vercel），或用环境变量 `CF_ZONE_ID` |
| `--dns-subdomain` | 子域名前缀（如 `cf` 会创建 `cf.example.com`） |
| `--dns-upload-count` | 上传 IP 数量（默认与 `--download-top` 相同） |

示例：

```bash
# Cloudflare（使用环境变量）
export CF_API_TOKEN="your_token"
export CF_ZONE_ID="your_zone_id"
./mcis --cidr-file ./ipv4cidr.txt --dns-provider cloudflare --dns-subdomain cf -v

# Vercel
./mcis --cidr-file ./ipv4cidr.txt --dns-provider vercel --dns-zone example.com --dns-subdomain cf --dns-token YOUR_TOKEN -v
```

## 自带网段文件

仓库自带 Cloudflare 高可见度网段（从 `bgp.he.net/AS13335` 抓取，visibility > 90%）：

- `ipv4cidr.txt`：IPv4 网段
- `ipv6cidr.txt`：IPv6 网段

更新日期：2026-02-04。

## CIDR 文件格式

- 每行一个 CIDR
- 支持空行和 `#` 注释

```text
# IPv4
1.1.0.0/16
1.0.0.0/16

# IPv6
2606:4700::/32
```

## 输出格式说明

### text 格式

每行包含：rank、ip、score_ms、ok/status、prefix、colo

### jsonl 格式

一行一个 JSON，包含完整字段：ip、prefix、ok、status、connect_ms、tls_ms、ttfb_ms、total_ms、score_ms、trace 等

### csv 格式

常用字段列，适合导入表格分析。

## 常见问题

**Q: 为什么全部 `ok=false`？**

常见原因：
- 网络无法直连到目标 IP 的 443 端口
- 本地防火墙拦截
- 目标不支持当前 host/path 组合

建议：调大 `--timeout`，先用默认的 `example.com` + `/cdn-cgi/trace` 测试。

**Q: 代理环境下能用吗？**

本工具**强制直连**，忽略 `HTTP_PROXY/HTTPS_PROXY/NO_PROXY` 环境变量，确保测速结果不被代理影响。

## 构建

需要 Go 1.25+：

```bash
go build -o mcis ./cmd/mcis
```

## License

GNU General Public License v3.0（GPL-3.0）
