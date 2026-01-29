# Monte Carlo IP Searcher（mcis）

一个 **Cloudflare IP 优选**工具：用**层次化 Thompson Sampling + 多头分散搜索**，在更少探测次数下，从 IPv4/IPv6 网段里找到更快/更稳定的 IP。

v0.2.0 采用贝叶斯优化算法，自动平衡"探索"与"利用"，无需手动调参。

v0.2.1 新增多次测试取平均功能，跳过首次握手开销，提供更稳定准确的延迟测量。**重要变化**：v0.2.1 之前的延迟结果包含 TCP/TLS 握手开销；v0.2.1 及之后显示的是跳过握手后的真实 TCP/TLS 延迟（连接复用后的延迟），数值会更低且更准确。

示例优选域名：`hao.haohaohao.xyz`

[Release](https://github.com/Leo-Mu/montecarlo-ip-searcher/releases/latest) 下载解压后在文件夹中右键打开终端。

IPv4 和 IPv6 的命令分别为：

```bash
./mcis -v --out text --cidr-file ./ipv4cidr.txt
```

```bash
./mcis -v --out text --cidr-file ./ipv6cidr.txt
```

从源码运行：

```bash
go run ./cmd/mcis -v --out text --cidr-file ./ipv4cidr.txt
```

```bash
go run ./cmd/mcis -v --out text --cidr-file ./ipv6cidr.txt
```

注意，本项目使用的是 https 真返回测速。**v0.2.1 之前**：延迟包含 TCP/TLS 握手开销；**v0.2.1 及之后**：默认跳过第1次握手，取第2到第n+1次的平均延迟，显示的是复用连接后的真实 TCP/TLS 延迟（更准确）。使用你的网站作为 `--host`（同时用于 SNI 和 Host header），可以保证优选出来的 ip 当前在你的区域一定对你的网站生效，如有特殊需求还可自定义 path。

推荐在晚高峰时段运行测试，因为本项目采用不同 IP 之间的延迟差异来缩小查找范围，差异越小，收敛越困难。

## 特色

- **Thompson Sampling**：贝叶斯优化算法，自动平衡探索与利用，无需手动调参（相比 UCB 算法）。
- **递进式下钻**：不是全段扫描，而是对表现更好的子网逐步"下钻拆分"，把预算集中到更有潜力的区域。
- **多头分散探索**：多个搜索头并行探索不同区域，通过"排斥力"机制避免收敛到同一局部最优。
- **层次化统计**：每个前缀维护独立的贝叶斯后验分布，支持快速识别优质子网。
- **IPv4 / IPv6 同时支持**：CIDR 解析、拆分、采样、探测全流程支持 v4/v6 混合输入。
- **强制直连探测**：即使系统/环境变量配置了代理，本工具也会**忽略 `HTTP_PROXY/HTTPS_PROXY/NO_PROXY`**，确保测速不被代理污染。
- **多次测试取平均**（v0.2.1 新功能）：每个IP默认测试6次，跳过第1次（包含TCP/TLS握手开销），取第2-6次的平均延迟，提供更稳定准确的测量结果。
- **探测方式**：默认对 `https://example.com/cdn-cgi/trace` 发起请求，域名可用 `--host` 覆盖，也可分别用 `--sni` / `--host-header` 覆盖 tls sni 和 http Host header ；路径可使用 `--path` 覆盖。
- **输出格式**：支持 `jsonl` / `csv` / `text`。
- **DNS 上传功能**：搜索和测速完成后，可将优选 IP 自动上传到 DNS 服务商（支持 Cloudflare 和 Vercel），作为同一子域名的多条 A/AAAA 记录，实现自动化部署。

## 快速开始

### 1）用单个 CIDR（IPv4）

```bash
./mcis --cidr 1.1.1.0/24 -v --out text
```

### 2）用单个 CIDR（IPv6）

```bash
./mcis --cidr 2606:4700::/32 -v --out text
```

### 3）从文件读取多个 CIDR（IPv4/IPv6 混合）

```bash
./mcis --cidr-file cidrs.txt -v --out text
```

## 参数详解

- `--cidr`：输入 CIDR（可重复）
- `--cidr-file`：从文件读取 CIDR
- `--budget`：总探测次数（越大越稳，但更耗时）
- `--concurrency`：并发探测数量
- `--top`：输出 Top N IP
- `--timeout`：单次探测超时（如 `2s` / `3s`，注意：总超时时间 = 单次超时 × 测试次数）
- `--heads`：多头数量（分散探索）
- `--beam`：每个 head 保留的候选前缀数量（越大越“发散”）
- `--min-samples-split`：前缀至少采样多少次才允许下钻拆分（默认 5）
- `--split-interval`：每多少个样本检查一次拆分机会（默认 20）
- `--diversity-weight`：多头多样性权重（0-1，越高越分散探索，默认 0.3）
- `--split-step-v4`：IPv4 下钻时前缀长度增加步长（例如 `/16 -> /18` 用 `2`）
- `--split-step-v6`：IPv6 下钻时前缀长度增加步长（例如 `/32 -> /36` 用 `4`）
- `--max-bits-v4` / `--max-bits-v6`：限制下钻到的最细前缀
- `--host`：同时设置 TLS SNI 与 HTTP Host header（默认 `example.com`）
- `--sni`：TLS SNI（已弃用：推荐用 `--host`）
- `--host-header`：HTTP Host（已弃用：推荐用 `--host`）
- `--path`：请求路径（默认 `/cdn-cgi/trace`）
- `--rounds`：每个IP的测试次数（默认 6，建议保持默认值）
- `--skip-first`：计算平均值时跳过前N次测试（默认 1，跳过第1次握手开销）
- `--out`：输出格式 `jsonl|csv|text`
- `--out-file`：输出到文件（默认 stdout）
- `--seed`：随机种子（0 表示使用时间种子）
- `-v`：输出进度到 stderr

### 按 colo 过滤 CDN 节点

探测请求的响应（如 `/cdn-cgi/trace`）中若包含 `colo` 字段，表示该 IP 对应的 CDN 机房代码（如 Cloudflare 的 `HKG`、`SJC` 等）。可通过以下参数只保留或排除指定机房的节点进入最终 Top N 结果：

- `--colo`：**白名单**，逗号分隔的 colo 列表；仅这些机房的节点会进入结果。例如：`--colo HKG,SJC`
- `--colo-exclude`：**黑名单**，逗号分隔的 colo 列表；这些机房的节点不会进入结果。例如：`--colo-exclude LAX,DFW`

未设置时不过滤，所有探测成功的节点按延迟参与排名。`--colo` 与 `--colo-exclude` 只能二选一，不能同时使用。

示例（只保留香港和圣何塞节点）：

```bash
./mcis -v --out text --cidr-file ./ipv4cidr.txt --colo HKG,SJC
```

### 下载速度测试参数（对前几名 IP 测速）

搜索结束后，可对排名靠前的 IP 进行**下载速度测试**（默认 URL：`https://speed.cloudflare.com/__down?bytes=50000000`）。

- `--download-top`：对 Top N IP 进行测速（默认 5，设为 0 关闭）
- `--download-bytes`：下载大小（默认 50000000 字节）
- `--download-timeout`：单个 IP 下载测速超时（默认 45s）

提示：

- 下载测速会消耗明显流量与时间（50MB/个 IP），建议先用小 N 验证。
- 本项目同样会**强制直连**并忽略代理环境变量，避免测速被代理扭曲。

### DNS 上传功能

搜索和测速完成后，可将优选 IP 自动上传到 DNS 服务商，作为同一子域名的多条 A/AAAA 记录。

支持的 DNS 服务商：

- **Cloudflare**
- **Vercel**

#### DNS 上传参数

- `--dns-provider`：DNS 服务商（`cloudflare` 或 `vercel`）
- `--dns-token`：API Token（也可用环境变量 `CF_API_TOKEN` / `VERCEL_TOKEN`）
- `--dns-zone`：Cloudflare Zone ID 或 Vercel 域名（也可用环境变量 `CF_ZONE_ID`）
- `--dns-subdomain`：子域名前缀（如 `cf` 会创建 `cf.example.com`）
- `--dns-upload-count`：上传 IP 数量（默认与 `--download-top` 相同）
- `--dns-team-id`：Vercel Team ID（可选，也可用环境变量 `VERCEL_TEAM_ID`）

#### 工作流程

1. 只从经过下载测速的 IP（前 `--download-top` 个）中选择
2. 按下载速度（Mbps）降序排序
3. 删除该子域的所有同类型旧记录（A 或 AAAA）
4. 创建新的 DNS 记录

#### 示例

Cloudflare（使用命令行参数）：

```bash
./mcis --cidr-file ./ipv4cidr.txt --dns-provider cloudflare --dns-zone YOUR_ZONE_ID --dns-subdomain cf --dns-token YOUR_API_TOKEN -v
```

Cloudflare（使用环境变量）：

```bash
# 先设置环境变量
export CF_API_TOKEN="your_token"
export CF_ZONE_ID="your_zone_id"

# 然后运行
./mcis --cidr-file ./ipv4cidr.txt --dns-provider cloudflare --dns-subdomain cf -v
```

Vercel：

```bash
./mcis --cidr-file ./ipv4cidr.txt --dns-provider vercel --dns-zone example.com --dns-subdomain cf --dns-token YOUR_VERCEL_TOKEN -v
```

只上传前 3 个最快的 IP：

```bash
./mcis --cidr-file ./ipv4cidr.txt --download-top 5 --dns-upload-count 3 --dns-provider cloudflare --dns-subdomain cf -v
```

IPv6 优选并上传（会创建 AAAA 记录）：

```bash
./mcis --cidr-file ./ipv6cidr.txt --dns-provider cloudflare --dns-subdomain cf6 -v
```

IPv4 + IPv6 混合优选（A 和 AAAA 记录都会更新）：

```bash
./mcis --cidr-file ./ipv4cidr.txt --cidr-file ./ipv6cidr.txt --dns-provider cloudflare --dns-subdomain cf -v
```

## 项目自带网段（bgp.he.net 高可见度）

仓库内自带一份 **Cloudflare 实际在用（BGP 可见度高）**的网段列表：

- `ipv4cidr.txt`：**2026-01-23** 从 `bgp.he.net/AS13335` 抓取整理，筛选条件为 **visibility > 90%** 的 Cloudflare IPv4 前缀（每行一个 CIDR）。
- `ipv6cidr.txt`：**2026-01-23** 从 `bgp.he.net/AS13335` 抓取整理，筛选条件为 **visibility > 90%** 的 Cloudflare IPv6 前缀（每行一个 CIDR）。

说明：

- 该列表用于提供一个“更贴近实际在用”的候选搜索空间，减少在冷门/未广播段上的无效探测。
- BGP 可见度与实际可用性会随时间变化；建议你按需定期更新该文件。

## CIDR 文件格式（`--cidr-file`）

- 每行一个 CIDR
- 支持空行
- 支持 `#` 注释（行首或行尾）

示例 `cidrs.txt`：

```text
# v4
1.1.0.0/16
1.0.0.0/16

# v6
2606:4700::/32
```

## 输出说明

### `--out text`

每行：

- `rank`
- `ip`
- `score_ms`（越小越好；失败会被惩罚）
- `ok/status`
- `prefix`
- `colo`（若 trace 返回包含该字段）
- `dl_*`（可选）：若启用下载测速（见下方 `--download-top`），会追加 `dl_ok/dl_mbps/dl_ms` 等字段

### `--out jsonl`

一行一个 JSON，对应 `TopResult` 结构，包含：`ip/prefix/ok/status/connect_ms/tls_ms/ttfb_ms/total_ms/score_ms/trace/...`

### `--out csv`

包含常用字段列，适合直接导入表格分析。

## 代理/直连说明（重要）

本工具探测时**强制直连**：即使你设置了环境变量（如 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY`），也不会生效。

如果你希望“走代理”测速，请不要用本项目的探测器逻辑，或自行修改代码（`internal/probe/trace.go` 中 `Transport.Proxy` 被显式设为 `nil`）。

（这样设计是为了避免在系统代理环境下得到被代理扭曲的延迟/可用性结果。）

## 常见问题

### 为什么全部 `ok=false`？

常见原因：

- 网络无法直连到目标 IP 的 443
- 本地网络/防火墙拦截
- 目标 IP 不支持当前 `--sni/--host-header/--path` 组合

建议先把 `--timeout` 调大一点，并尝试使用默认参数（`example.com` + `/cdn-cgi/trace`）。

## 构建

Go 1.25+

在仓库根目录：

```bash
go test ./...
go build -o mcis ./cmd/mcis
```

Windows PowerShell 也可以直接：

```powershell
go build -o mcis.exe .\cmd\mcis
```

## License

本项目使用 **GNU General Public License v3.0（GPL-3.0）** 开源发布。

详见仓库根目录的 [`LICENSE`](./LICENSE) 文件。