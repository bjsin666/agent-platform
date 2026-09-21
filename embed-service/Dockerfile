# Python embedding 模型服务
# 构建: docker build -t agent-embed ./embed-service
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
# 清华源加速;torch 等依赖较大,首次构建较慢属正常
RUN pip install --no-cache-dir -i https://pypi.tuna.tsinghua.edu.cn/simple -r requirements.txt
COPY main.py .
# 本地模型(bge-small-zh,约 190MB)打进镜像
COPY models/ models/
EXPOSE 8001
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8001"]
