# flyer 仓库规则

- 发版是公开操作：先读 [RELEASING.md](RELEASING.md)；push tag、发布 npm、改仓库可见性需维护者明确授权。
- 仓库保持 public；private 会让 installer 下载 Release 资产 404。
- 版本随 PR 提交；tag 打在合并后的 main 上，与 `npm/package.json` 版本一致；已发布 tag 不动。
- npm 只用 OIDC staged publish；2FA approve 由维护者完成，agent 不代输。
