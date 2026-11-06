@echo off
setlocal
cd /d "%~dp0"

set OUTFILE=build\OpenListMounter.exe
set STAGING=build\olm_staging.exe

echo [1/4] Building...
if not exist build mkdir build
if exist "%STAGING%" del /f /q "%STAGING%"
go build -ldflags="-s -w -H windowsgui" -o "%STAGING%" .
if errorlevel 1 goto fail

echo [2/4] Patching icon resource...
go run ./tools/iconpatch "%STAGING%" openlist.ico
if errorlevel 1 goto fail

echo [3/4] Replacing output...
if exist "%OUTFILE%" del /f /q "%OUTFILE%"
move /Y "%STAGING%" "%OUTFILE%" >nul
if errorlevel 1 goto fail

echo [4/4] Copying docs...
copy /Y README.md build\README.md >nul
copy /Y openlist.ico build\openlist.ico >nul

echo.
echo Done: %OUTFILE%
exit /b 0

:fail
echo.
echo BUILD FAILED
if exist "%STAGING%" del /f /q "%STAGING%"
exit /b 1
