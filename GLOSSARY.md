# flyer

flyer 把 HTML 发布为按 slug 寻址、可过期、可加密码保护的 Page，并用 API key 控制谁能发布与管理 Page。

## Language

**Page**:
一份已发布的 HTML，按 slug 寻址，可设置 TTL 与访问密码。
_Avoid_: 页面、文档、文件

**TTL**:
Page 从发布到过期的时长。
_Avoid_: 有效期、过期时间、lifetime

**默认 TTL**:
未显式指定 TTL 时，服务端为 Page 采用的存活时长。
_Avoid_: 缺省过期时间、默认有效期

**Update**:
用新 HTML 原地替换既有 Page 的内容；slug、链接、owner、密码和 created_at 不变，TTL 默认不变。
_Avoid_: 重新发布、覆盖发布
