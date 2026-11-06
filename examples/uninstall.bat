@echo off
chcp 936 >nul
cd /d "%~dp0.."

net session >nul 2>&1
if %errorlevel% neq 0 (
    echo [错误] 需要管理员权限。请右键本文件选择"以管理员身份运行"。
    pause
    exit /b 1
)

echo 正在停止并卸载 WinCron 服务...
wincron.exe stop
wincron.exe uninstall
echo 完成。
pause
