@echo off
cd /d "%~dp0"
py -3.12 -m uvicorn main:app --port 8001