# escape=`
FROM mcr.microsoft.com/windows/nanoserver:ltsc2022@sha256:e4ce8c20390c3785c3cbeef15c579d186b3599d37525c596590cf4508e38d3ff

WORKDIR C:\pasturestack
COPY metadata-service.exe C:\pasturestack\metadata-service.exe
COPY start.cmd C:\pasturestack\start.cmd

USER ContainerAdministrator
ENTRYPOINT ["C:\\pasturestack\\start.cmd"]
CMD ["--subscribe"]
