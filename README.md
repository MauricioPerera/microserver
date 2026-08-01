# microserver

Servidor HTTP en Go que expone búsqueda semántica sobre SQLite (`sqlite-vec`), usando `embeddinggemma` vía Ollama para generar los embeddings.

## Requisitos

- Go 1.26+, con cgo habilitado (gcc/mingw en Windows).
- [Ollama](https://ollama.com) corriendo en `localhost:11434` con el modelo `embeddinggemma` descargado.
- Variables de entorno `AUTH_USERNAME` y `AUTH_PASSWORD` — el servidor no arranca sin ambas.

### Instalar Ollama

**Windows:**
```bash
winget install Ollama.Ollama
```
O descargar el instalador desde [ollama.com/download](https://ollama.com/download).

**macOS:**
```bash
brew install ollama
```
O descargar el `.dmg` desde [ollama.com/download](https://ollama.com/download).

**Linux:**
```bash
curl -fsSL https://ollama.com/install.sh | sh
```

En Windows y macOS el instalador deja Ollama corriendo como servicio automáticamente. En Linux, si no arrancó solo:
```bash
ollama serve
```

### Descargar el modelo de embeddings

```bash
ollama pull embeddinggemma
```

Verificar que quedó disponible:
```bash
ollama list
```

## Correr el servidor

```bash
AUTH_USERNAME=admin AUTH_PASSWORD=cambia-esto go run .
```

Escucha en `:8080`. Crea/usa `vec.db` en el directorio actual. Corre en background: checkpoint del WAL cada 5 min, backup rotado (`backups/`, conserva 7) cada 1h. `Ctrl+C` hace un checkpoint y backup final antes de salir.

## Build

Las dependencias están vendorizadas (`vendor/`), así que `go build`/`go test` no necesitan red ni tocar el module cache — Go usa `vendor/` automáticamente si el directorio existe.

```bash
go build -o microserver.exe .   # Windows
go build -o microserver .       # Linux/macOS
```

Requiere `CGO_ENABLED=1` (es el default si Go detecta un compilador C) y gcc/mingw disponible — el binario enlaza estáticamente SQLite + la extensión `sqlite-vec` vía cgo.

**Cross-compilation:** no funciona con solo `GOOS`/`GOARCH` como en un build 100% Go. Al usar cgo, compilar para un SO o arquitectura distinta a la máquina actual requiere un cross-compilador de C para el target (ej. `zig cc`, o un toolchain mingw/gcc específico). Lo más simple y confiable es compilar nativamente en cada plataforma destino.

Verificar el binario antes de desplegar:
```bash
go vet ./...
go test ./...
```

## Despliegue

El binario resultante es autocontenido (SQLite y `sqlite-vec` quedan enlazados adentro) — no depende de instalar SQLite por separado en el servidor destino. Lo que sí necesita en el destino:

1. **Ollama corriendo con `embeddinggemma`**, alcanzable en la URL de `OLLAMA_URL` (default `http://localhost:11434/api/embed`, válido solo si ambos procesos comparten el mismo network namespace — no vale dentro de un contenedor, ver sección Docker).
2. **Directorio de trabajo con permisos de escritura** — ahí se crean `vec.db`, `vec.db-wal`, `vec.db-shm` y `backups/`. Correr el binario siempre desde el mismo directorio (o fijar working directory en el servicio) para no perder el archivo de datos entre reinicios.
3. **Puerto `8080` libre en loopback** — el proceso escucha en `127.0.0.1:8080` por default, no en todas las interfaces. Se cambia con la variable de entorno `HTTP_ADDR` (ej. `HTTP_ADDR=0.0.0.0:8080` si el reverse proxy corre en otro contenedor/host y necesita alcanzarlo por red).

### Pasos

```bash
go build -o microserver .
scp microserver usuario@servidor:/opt/microserver/
ssh usuario@servidor
cd /opt/microserver
./microserver
```

### Correr como servicio (Linux, systemd)

```ini
# /etc/systemd/system/microserver.service
[Unit]
Description=microserver
After=network.target ollama.service

[Service]
WorkingDirectory=/opt/microserver
ExecStart=/opt/microserver/microserver
Restart=on-failure
KillSignal=SIGTERM
TimeoutStopSec=15

[Install]
WantedBy=multi-user.target
```

`TimeoutStopSec` da margen para que el shutdown ordenado (checkpoint + backup final que ya hace `main.go` al recibir SIGTERM) termine antes de que systemd mate el proceso.

```bash
sudo systemctl enable --now microserver
```

### TLS (reverse proxy)

El binario en sí solo habla HTTP plano en `127.0.0.1:8080` — nunca queda expuesto directo a internet. TLS lo termina un reverse proxy delante. Se documenta con [Caddy](https://caddyserver.com) porque saca certificados de Let's Encrypt y los renueva solo, sin configurar certbot ni cronjobs; si ya usás nginx en tu infraestructura, el mismo patrón aplica con `proxy_pass` + certbot.

**Instalar Caddy (Linux, Debian/Ubuntu):**
```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update && sudo apt install caddy
```
Otras plataformas: [caddyserver.com/docs/install](https://caddyserver.com/docs/install).

**Config** (`/etc/caddy/Caddyfile`):
```
tu-dominio.com {
    reverse_proxy 127.0.0.1:8080
}
```

Con un dominio real apuntando al servidor por DNS, Caddy obtiene y renueva el certificado automáticamente al arrancar — no hace falta tocar nada más.

```bash
sudo systemctl reload caddy
```

**Verificar:**
```bash
curl https://tu-dominio.com/health
```

El puerto `8080` sin TLS solo es alcanzable desde el propio servidor (`127.0.0.1`) — cualquier cliente externo pasa obligatoriamente por Caddy en 443.

**Sin dominio / pruebas locales:** Caddy puede usar un certificado autofirmado con `tls internal` en vez del dominio:
```
localhost {
    tls internal
    reverse_proxy 127.0.0.1:8080
}
```

### Docker

> Verificado en un VPS Linux real: `docker build` limpio, arranque sin credenciales falla como debe, insert+search de punta a punta contra Ollama del host vía `--add-host=host.docker.internal:host-gateway`, y los datos sobreviven un `docker restart` gracias al volumen.

```bash
docker build -t microserver .
```

**El punto que rompe si no se configura:** dentro del contenedor, `localhost` es el contenedor mismo — no llega a un Ollama corriendo en el host. Hay que apuntar `OLLAMA_URL` a donde Ollama sea alcanzable de verdad:

- **Docker Desktop (Mac/Windows):** `host.docker.internal` funciona out of the box.
  ```bash
  docker run -d \
    -e AUTH_USERNAME=admin -e AUTH_PASSWORD=cambia-esto \
    -e OLLAMA_URL=http://host.docker.internal:11434/api/embed \
    -p 8080:8080 \
    -v microserver-data:/data \
    microserver
  ```
- **Linux:** `host.docker.internal` no resuelve solo — hay que agregarlo:
  ```bash
  docker run -d \
    --add-host=host.docker.internal:host-gateway \
    -e AUTH_USERNAME=admin -e AUTH_PASSWORD=cambia-esto \
    -e OLLAMA_URL=http://host.docker.internal:11434/api/embed \
    -p 8080:8080 \
    -v microserver-data:/data \
    microserver
  ```
- **Ollama en su propio contenedor**, en la misma red Docker: `OLLAMA_URL=http://ollama:11434/api/embed` (usando el nombre del servicio/contenedor en vez de `host.docker.internal`).

`AUTH_USERNAME`/`AUTH_PASSWORD` son obligatorias — el contenedor no arranca sin ellas, igual que corriendo el binario directo. El volumen (`-v microserver-data:/data`) persiste `vec.db`, `vec.db-wal/-shm` y `backups/` entre recreaciones del contenedor; sin él, se pierde todo al hacer `docker rm`.

`-p 8080:8080` publica el puerto directo, sin TLS. Para TLS con reverse proxy en Docker, la forma correcta es **no** publicar el puerto de `microserver` al host — ponerlo en una red Docker interna y dejar que un contenedor de Caddy (con su puerto 443 sí publicado) lo alcance por nombre de servicio, no por loopback.

### Respaldo del dato entre despliegues

Antes de reemplazar el binario o la máquina, copiar `vec.db` y `backups/` — son el estado; el binario es solo código.

## Endpoints

Todas las respuestas son JSON. Los cuerpos de request van sin encabezado `Content-Type` estricto (curl con `-d` funciona tal cual).

`GET /health` y `POST /login` son públicos. Todo lo demás (`/items*`, `/search`) requiere el header `Authorization: Bearer <token>` obtenido de `/login`.

### `GET /health`

Chequeo de salud. Público.

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### `POST /login`

Autentica con las credenciales configuradas en `AUTH_USERNAME`/`AUTH_PASSWORD` y devuelve un token firmado (HMAC), válido por 1 hora. Público.

**Body:**
```json
{"username": "admin", "password": "cambia-esto"}
```

**Respuesta:** `200 OK`
```json
{"token": "<base64-payload>.<base64-hmac-signature>", "expires_in": 3600}
```

**Errores:** `401` si el usuario o contraseña son incorrectos. `400` si el JSON es inválido.

```bash
TOKEN=$(curl -s -X POST http://localhost:8080/login -d '{"username":"admin","password":"cambia-esto"}' | jq -r .token)
curl http://localhost:8080/items -H "Authorization: Bearer $TOKEN"
```

El token es stateless (sin sesión en el servidor) — se firma con una clave aleatoria generada en memoria al arrancar el proceso, así que **todos los tokens quedan inválidos si el servidor se reinicia**; hay que loguearse de nuevo.

### `POST /items` — requiere auth

Inserta un texto: se guarda tal cual (columna auxiliar, no indexada) y se genera su embedding (`embeddinggemma`, 768 dims), guardado en float32 + binario cuantizado.

**Body:**
```json
{"text": "el gato duerme en el sofá", "id": 123}
```
`id` es opcional — si se omite o es `0`, SQLite asigna uno automáticamente.

**Respuesta:** `201 Created`
```json
{"id": 123}
```

**Errores:** `400` si falta `text` o el JSON es inválido.

```bash
curl -X POST http://localhost:8080/items -H "Authorization: Bearer $TOKEN" -d '{"text":"el gato duerme en el sofá"}'
```

### `GET /items` — requiere auth

Lista los items existentes (id + texto).

**Query params:**
| Param | Default | Descripción |
|---|---|---|
| `limit` | 100 | Máx 1000 |
| `offset` | 0 | Para paginar |

**Respuesta:** `200 OK`
```json
[{"id": 1, "text": "el gato duerme en el sofá"}, {"id": 2, "text": "un felino descansa sobre el mueble"}]
```

```bash
curl "http://localhost:8080/items?limit=10&offset=0" -H "Authorization: Bearer $TOKEN"
```

### `PUT /items/{id}` — requiere auth

Reemplaza el texto de un item existente (re-embebe y sobreescribe sus vectores).

**Body:**
```json
{"text": "un felino descansa sobre el mueble"}
```

**Respuesta:** `200 OK` con `{"id": N}`. `404` si el id no existe. `400` si falta `text` o el id no es entero.

```bash
curl -X PUT http://localhost:8080/items/123 -H "Authorization: Bearer $TOKEN" -d '{"text":"un felino descansa sobre el mueble"}'
```

### `DELETE /items/{id}` — requiere auth

Borra un item.

**Respuesta:** `204 No Content` si se borró. `404` si el id no existía. `400` si el id no es entero.

```bash
curl -X DELETE http://localhost:8080/items/123 -H "Authorization: Bearer $TOKEN"
```

### `GET /search` — requiere auth

Búsqueda semántica por similitud (KNN).

**Query params:**
| Param | Default | Descripción |
|---|---|---|
| `q` | — | Requerido. Texto de la consulta. |
| `limit` | 10 | Cantidad de resultados |
| `rerank` | `true` | Ver modos abajo |

**Respuesta:** `200 OK`
```json
[{"id": 1, "text": "el gato duerme en el sofá", "distance": 0.750674}, {"id": 2, "text": "un felino descansa sobre el mueble", "distance": 0.76079}]
```

**Modos de `rerank`:**
- `rerank=true` (default): filtro grueso binario (Hamming) + re-ranking exacto con los vectores float32. Calidad completa. `distance` es L2 exacto.
- `rerank=false`: solo el filtro binario, sin re-ranking. ~20x más rápido a escala (medido: 300k vectores → 9.4ms vs 189.6ms), ~5% menos preciso en tema correcto. `distance` es Hamming (entero, no comparable con el modo `rerank=true`).

```bash
curl "http://localhost:8080/search?q=un+gato+tomando+una+siesta&limit=5" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/search?q=un+gato+tomando+una+siesta&limit=5&rerank=false" -H "Authorization: Bearer $TOKEN"
```

**Errores:** `400` si falta `q`, o si `limit`/`rerank` son inválidos.

## Notas de arquitectura

- **Escrituras**: un único pool de conexión (`SetMaxOpenConns(1)`) serializa los inserts/updates/deletes — SQLite nunca permite escritores concurrentes reales; esto evita errores de "database is locked" en vez de intentar paralelizar lo que no se puede.
- **Lecturas**: pool separado con varias conexiones, no bloqueado por el escritor (modo WAL).
- **UPDATE**: implementado como `DELETE` + `INSERT` en una transacción, no como `UPDATE` SQL directo — `vec0` (la virtual table de `sqlite-vec`) no reconoce correctamente el tipo binario cuantizado en su path de `UPDATE`.
