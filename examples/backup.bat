@echo off
chcp 936 >nul
rem WinCron 备份任务示例：GBK 编码 + CRLF 换行，无窗口、无交互、全绝对路径
set "SRC=D:\data"
set "DST=\\nas\backup\daily"
set "LOG=C:\scripts\logs\backup.log"

if not exist "C:\scripts\logs" mkdir "C:\scripts\logs"
echo [%date% %time%] 备份开始 >> "%LOG%"

"C:\Windows\System32\robocopy.exe" "%SRC%" "%DST%" /MIR /R:2 /W:5 /NP /LOG+:"%LOG%"
if %ERRORLEVEL% GEQ 8 (
    echo [%date% %time%] 备份失败，退出码 %ERRORLEVEL% >> "%LOG%"
    exit /b 1
)

echo [%date% %time%] 备份完成 >> "%LOG%"
echo 备份完成
exit /b 0
