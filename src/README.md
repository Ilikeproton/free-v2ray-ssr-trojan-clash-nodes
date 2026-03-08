# 代理服务器测速与自动连接工具  大雄链（daxionglink）

## 📌 功能概述
本工具用于搜索网上公开的服务器资源，并自动测速多协议(vless,vmess,shadowsocks,trojan)代理服务器，选择最快的服务器进行连接。
简单高效创建socket5代理通道。适合多重socket，多目标优先路由等更复杂任务。

免费高效。支持
vless 
vmess 
shadowsocks 
trojan

- **软件目标**:本软件目标是高速高效，最少资源做最多事情。
- **测速模式（c）**：从 `server.txt` 读取服务器列表，进行测速，并将结果排序后保存。
- **快速连接模式（q）**：从 `servertested.txt` 选择最快的服务器，生成 Xray 配置并启动。创建端口10501的socket5端口。
- **配置文件（config.txt）**：可以自定义 SOCKS5 端口、测速URL、快速连接的服务器文件等。

---

## 🚀 安装指南

### 0. 下载zip文件，exe文件直接使用
下载zip文件
解压到任意地方，建议放在英文目录下。
使用cmd进入命令行
执行
```cmd
daxionglink.exe c
```
等待测速，默认10并发测速，在config.txt 设置

```cmd
daxionglink.exe q
```
在10501端口创建代理服务器，可使用浏览器插件链接或者支持 socket5 代理的软件链接。
可以在config.txt 设置相关内容。
server.txt 本文件会24-48小时在线更新，可自行更新。下载替换旧的。



### 1. 编译(不会编程的最好不要看，浪费时间)
以下请自行研究。

本工具基于 **Go** 语言开发，首先确保已安装 Go（1.16+）：

```sh
# 安装 Go（如果尚未安装）
sudo apt install golang  # Ubuntu
yay -S go               # Arch Linux
brew install go         # macOS
```

然后克隆代码并编译：

```sh
git clone https://github.com/your-repo/xray-speed-test.git
cd xray-speed-test
go build -o mytool main.go
```

> Windows 用户可以直接运行 `go build -o mytool.exe main.go`
> Windows 用户可以直接运行 `go build -o mytool.exe`

### 2. 下载 Xray 核心
本工具依赖 Xray-Core，请确保已下载并放置在同一目录下：

- [Xray-Core 下载地址](https://github.com/XTLS/Xray-core/releases)
- 下载后，解压 `xray` 可执行文件（Linux/macOS）或 `xray.exe`（Windows）到工具目录。

---

## 🛠️ 使用指南

### 1. 准备 `server.txt`
将要测速的服务器写入 `server.txt`，每行一个链接，例如：

```txt
vmess://eyJhZGQiOiIxOTIuMTY4LjAuM...  # vmess
vless://53fa8faf-ba4b-4322-9c69-a3e...  # vless
ss://YWVzLTI1Ni1nY206VEV6amZBWXEySWp...  # shadowsocks
trojan://password@server.com:443?se...  # trojan
```

### 2. 配置 `config.txt`
可选地编辑 `config.txt` 来自定义端口、测试 URL 和快速连接的服务器文件：

```txt
port=10501
qserver=servertested.txt
testurl=https://fast.com
downloadlink=http://ipv4.download.thinkbroadband.com/5MB.zip
```

- **port**：本地 SOCKS5 代理端口（默认 `10501`）。
- **qserver**：快速连接时读取的服务器文件（默认 `servertested.txt`）。
- **testurl**：测速时访问的测试 URL（用于简单连通性测试）。
- **downloadlink**：下载测速 URL。如果设置了此项，测速模式将下载该文件并计算真实的下载速度（MB/s），并按速度从快到慢排序。

### 3. 运行测速模式（c）

```sh
mytool c
```

- 读取 `server.txt` 中的服务器，测试其连接速度。
- 结果按速度排序后保存至 `servertested.txt`（仅包含链接）。
- 详细测速信息（含时间、速度、状态）保存在 `result.txt`。

### 4. 运行快速连接模式（q）

```sh
mytool q
```

- 读取 `config.txt` 中 `qserver` 设定的文件（默认 `servertested.txt`）。
- 取第一行（最快的服务器），生成 `config.json`。
- 启动 Xray 并在本地 `port` 端口创建 SOCKS5 代理。

---

## 📂 目录结构
```sh
xray-speed-test/
├── mytool              # 可执行文件（Linux/macOS）
├── mytool.exe          # 可执行文件（Windows）
├── xray                # Xray-Core（Linux/macOS）
├── xray.exe            # Xray-Core（Windows）
├── server.txt          # 服务器列表
├── servertested.txt    # 排序后的测速结果（仅服务器链接）
├── result.txt          # 详细测速信息（时间、速度等）
├── config.txt          # 用户自定义配置
└── README.md           # 本文档
```

---

## ⚠️ 注意事项
- 需确保 `xray` 可执行文件位于当前目录，或者已加入 `PATH`。
- 测速期间 Xray 可能会创建多个临时 `config_x.json` 文件，测速完成后会自动删除。
- Windows 用户运行 `mytool.exe` 需在 CMD 或 PowerShell 终端内。

---

## 🔥 未来优化方向
- 添加 GUI 界面，方便用户使用。
- 增加 `trojan://` 和 `shadowsocks://` 解析优化。
- 让用户可以选择测速策略，例如 TCP 还是 UDP 连接。

---

## 🎯 贡献 & 反馈
如果遇到问题或有改进建议，欢迎提交 Issue 或 Pull Request！






