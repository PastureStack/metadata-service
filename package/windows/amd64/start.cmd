@echo off
setlocal
netsh interface ipv4 add address name="Ethernet" address=169.254.169.250 mask=255.255.255.255 store=active >NUL 2>&1
C:\pasturestack\metadata-service.exe %*
exit /b %ERRORLEVEL%
