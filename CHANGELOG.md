# Changelog

Formato basado en [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

Todo esto está en `master`, sin cortar como release todavía.

### Added
- Búsqueda de texto completo (FTS5) sobre el campo `text` de colecciones con vector: `GET /collections/{name}/fulltext`, sintaxis FTS5 completa (`AND`/`OR`/`NOT`, `"frase exacta"`, prefijo `*`), ranking por `bm25`. **Requiere el build tag `sqlite_fts5` en todo el proyecto** — sin él, cualquier colección con vector falla al crearse, no solo el full-text.
- Agregaciones sobre campos de `data`: `GET /collections/{name}/aggregate` (`count`, `sum`, `avg`, `min`, `max`, `group_by` opcional).
- Orden por campo de `data`: `sort=campo` / `sort=-campo` en `GET .../items` y `GET .../search`.
- Filtrado por campos de `data`: `campo=valor` / `campo__op=valor` (`eq`, `ne`, `lt`, `lte`, `gt`, `gte`, `like`) en `GET .../items` y `GET .../search`.
- Referencias entre colecciones: `references` en `POST /collections`, con `on_delete: restrict|set_null`. Nuevo `GET /collections/{name}/items/{id}` para resolver referencias por id.
- Colecciones dinámicas: `POST /collections` (con o sin vector, JSON libre en `data`), `GET /collections`, `DELETE /collections/{name}`, y CRUD + búsqueda por colección.
- Columna `text` en `vec_items` — `/items` y `/search` devuelven el texto original, no solo id/distance.

## [0.1.0] - 2026-08-01

Primer release. Servidor HTTP en Go sobre SQLite (`sqlite-vec`) con `embeddinggemma` (Ollama) para embeddings.

### Added
- `vec_items`: tabla fija con búsqueda semántica (`POST/GET/PUT/DELETE /items`, `GET /search`), rerank opcional (binario rápido vs float32 exacto).
- Auth: `POST /login` con token HMAC stateless; protege todo salvo `/health` y `/login`.
- Concurrencia: writer serializado, pool de lectura separado, WAL + busy_timeout.
- Mantenimiento: checkpoint de WAL y backups rotados, ambos periódicos.
- Despliegue: Dockerfile (verificado en VPS real), TLS vía reverse proxy (Caddy), systemd.
- CI: GitHub Actions con Ollama real, badges de build/cobertura/licencia/versión de Go/tamaño de binario y de imagen Docker.
- Licencia MIT.
