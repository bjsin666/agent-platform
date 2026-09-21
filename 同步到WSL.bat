@echo off
chcp 65001 >nul
echo ============================================
echo  把 Windows 的代码同步到 WSL（~/agent-platform）
echo ============================================
echo.
wsl.exe -e bash -lc "rsync -a --delete --exclude=models --exclude=.git --exclude=__pycache__ --exclude='*.log' --exclude='*.exe' '/mnt/c/Users/13635/Desktop/求职/agent-platform/' ~/agent-platform/ && echo 同步完成 && echo. && echo 差异检查（空=完全一致）: && diff -rq '/mnt/c/Users/13635/Desktop/求职/agent-platform' ~/agent-platform --exclude=models --exclude=__pycache__ --exclude='*.log' --exclude='*.exe' | head -5"
echo.
echo 提示：同步后如需生效，在 WSL 里执行  docker compose up -d --build
pause