# escape=`
FROM mcr.microsoft.com/windows/nanoserver:ltsc2022

WORKDIR C:\pasturestack
COPY metadata-service.exe C:\pasturestack\metadata-service.exe
COPY start.cmd C:\pasturestack\start.cmd

USER ContainerAdministrator
ENTRYPOINT ["C:\\pasturestack\\start.cmd"]
CMD ["--subscribe"]
