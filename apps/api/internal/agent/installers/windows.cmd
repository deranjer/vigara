@echo off
setlocal
set "VIGARA_INSTALLER=%~dp0install.ps1"
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "$p = Start-Process -FilePath powershell.exe -Verb RunAs -ArgumentList ('-NoProfile -ExecutionPolicy Bypass -NoExit -File ' + [char]34 + $env:VIGARA_INSTALLER + [char]34) -Wait -PassThru; exit $p.ExitCode"
if errorlevel 1 pause
endlocal
