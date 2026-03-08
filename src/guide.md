# Daxionglink 使用说明

## 目标

此工具有两个模式。
1. c 模式：测速并排序 server.txt 内的所有服务器。
2. q 模式：使用 servertested.txt 的第一行服务器，生成 Xray 配置并启动本地 SOCKS5 代理。

## 必备文件

将以下文件放在同一目录。
- mytool.exe 或 main.go 编译后的 exe
- xray.exe
- server.txt
- 可选：GeoLite2-City.mmdb, geoip.dat, geosite.dat

## config.txt 说明

config.txt 为纯文本，每行一个键值，格式如下。

~~~
key=value
~~~

支持参数如下。

- port：本地 SOCKS5 端口，默认 10501。
- testurl：测速 URL。用于轻量测试与延迟判断。
- downloadlink：下载测速 URL。设置后会覆盖 testurl，用于真实下载速度测速。
- connection 或 maxcon：并发测速数量，默认 5。
- core：核心程序名，支持 xray 或 sing-box。
- tserver：测速列表文件，默认 server.txt。
- qserver：快速连接列表文件，默认 servertested.txt。

### 重点：用 URL 测速时如何设置

测速优先级如下。
1. 如果配置了 downloadlink，会使用 downloadlink 进行下载测速（真实 MB/s）。
2. 如果没有 downloadlink，就使用 testurl 进行测速（连通性和响应为主）。

因此：
- 只想用某个网站测速：设置 testurl，保持 downloadlink 为空。
- 想测真实下载速度：设置 downloadlink 为直链文件 URL，testurl 可留默认。

### 示例配置

~~~
port=10501
connection=10
core=xray
tserver=server.txt
qserver=servertested.txt

testurl=https://fast.com
# 如果要下载测速，取消下面这行注释并填写直链
# downloadlink=http://ipv4.download.thinkbroadband.com/5MB.zip
~~~

## 使用方法

### 模式 c：测速并生成列表

~~~
mytool.exe c
~~~

结果：
- 生成 servertested.txt（按速度从快到慢的链接）
- 生成 result.txt（包含详细测速结果）
- 自动更新 config.txt 中的 qserver

### 模式 q：快速连接

~~~
mytool.exe q
~~~

结果：
- 在 config 目录生成对应的 Xray JSON 配置
- 启动 xray.exe 并监听本地 SOCKS5 端口

## 测试与连接

1. 先执行 c 模式，确保 servertested.txt 有内容。
2. 再执行 q 模式，观察控制台是否启动成功。
3. 使用 SOCKS5 代理：127.0.0.1:10501（端口以 config.txt 为准）。

## 常见问题

- xray 启动失败：检查 xray.exe 是否存在，端口是否被占用。
- 速度全失败：检查 server.txt 是否有效，尝试更换 testurl 或 downloadlink。
- 想用不同配置：执行 mytool.exe c myconfig，会读取 myconfig.txt。
