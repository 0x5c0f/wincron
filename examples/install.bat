@echo off
chcp 936 >nul
cd /d "%~dp0.."

net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [错误] 需要管理员权限。请右键本文件选择"以管理员身份运行"。
    pause
    exit /b 1
)

echo 正在安装 WinCron 服务...
wincron.exe install
if %errorlevel% neq 0 (echo [失败] 安装未完成 & pause & exit /b 1)

echo 正在启动服务...
wincron.exe start

echo.
echo 完成。查看状态: wincron status
echo 管理界面: http://localhost:9090
echo 环境诊断: wincron doctor
echo 服务环境快照: logs\environment.log
pause
