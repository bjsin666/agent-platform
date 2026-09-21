"""Agent 平台 embedding 服务:只做"文本 -> 向量"一件事。

切块/检索/入库逻辑一律在 Go 侧(见 plan0.md §9),本服务只负责用
本地 bge-small-zh 模型把文本编码为 512 维向量。
"""

from __future__ import annotations

from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
from sentence_transformers import SentenceTransformer

# 模型目录相对 embed-service 工作目录
MODEL_PATH = "models/bge-small-zh"
# 单次请求最大批量(与 Go 侧 embedding.BatchSize 对齐)
MAX_BATCH = 32

model: SentenceTransformer | None = None


class EmbedRequest(BaseModel):
    """请求体:非空文本数组,长度 1..MAX_BATCH。"""

    texts: list[str]


class EmbedResponse(BaseModel):
    """响应体:向量列表 + 维度。"""

    vectors: list[list[float]]
    dim: int


@asynccontextmanager
async def lifespan(_app: FastAPI):
    """启动时加载模型并预热,模型只加载一次。

    归一化在 encode() 调用处开启(sentence-transformers>=6.0 起不再接受
    构造参数):输出归一化向量,使余弦相似度等价于内积,便于 Go 侧用点积计算。
    """
    global model
    model = SentenceTransformer(MODEL_PATH)
    model.encode(["预热"], normalize_embeddings=True)
    yield
    model = None


app = FastAPI(title="agent-embed", lifespan=lifespan)


@app.get("/health")
def health() -> dict:
    """模型就绪时返回 ok;未加载返回 503。"""
    if model is None:
        raise HTTPException(status_code=503, detail="model not ready")
    return {"status": "ok"}


@app.post("/v1/embed")
def embed(req: EmbedRequest) -> EmbedResponse:
    """文本 -> 向量。空文本/超批量/全空白文本返回 400。"""
    if model is None:
        raise HTTPException(status_code=503, detail="model not ready")
    if not req.texts:
        raise HTTPException(status_code=400, detail="texts must not be empty")
    if len(req.texts) > MAX_BATCH:
        raise HTTPException(status_code=400, detail=f"batch size exceeds {MAX_BATCH}")
    cleaned = [t for t in req.texts if t and t.strip()]
    if not cleaned:
        raise HTTPException(status_code=400, detail="all texts are empty")
    vectors = model.encode(cleaned, normalize_embeddings=True).tolist()
    return EmbedResponse(vectors=vectors, dim=len(vectors[0]))
