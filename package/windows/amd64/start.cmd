@echo off
setlocal
if "%METADATA_ADDRESS%"=="" set "METADATA_ADDRESS=169.254.169.250"
netsh interface ipv4 add address name="Ethernet" address=%METADATA_ADDRESS% mask=255.255.255.255 store=active >NUL 2>&1
C:\pasturestack\metadata-service.exe %*
exit /b %ERRORLEVEL%
