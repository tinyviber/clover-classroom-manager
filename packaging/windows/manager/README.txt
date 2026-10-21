三叶草局域网课堂管理 · Clover LAN Classroom Manager
Windows x64 管理端
========================

- Clover-Classroom-Manager.exe       桌面管理端（Windows 10+，首次启动在当前用户
                                     数据目录生成管理端密钥和设备注册表；请定期
                                     备份该目录，管理端密钥用于学生端验证身份）
- Clover-Classroom-Manager-Win7.exe  headless 管理端（Windows 7+）：无窗口后台进程，
                                     启动后自动用默认浏览器打开管理界面；与桌面管理端
                                     共用同一数据目录，身份与设备注册表一致
- agent.exe                          学生端 agent（Windows 7+）：管理端「分发客户端」
                                     导出安装包时自动随包带出

学生机首次部署请在还原软件解冻状态下安装，确认管理端看到设备上线后再冻结。Windows 可能对未签名程序显示 SmartScreen 提示。
