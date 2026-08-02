# microserver

[![CI](https://github.com/MauricioPerera/microserver/actions/workflows/ci.yml/badge.svg)](https://github.com/MauricioPerera/microserver/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FMauricioPerera%2Fmicroserver%2Fbadges%2Fcoverage.json)](https://github.com/MauricioPerera/microserver/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/MauricioPerera/microserver)](go.mod)
[![Dependency licenses](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FMauricioPerera%2Fmicroserver%2Fbadges%2Fdeps-license.json)](go.mod)
[![Release](https://img.shields.io/github/v/release/MauricioPerera/microserver)](https://github.com/MauricioPerera/microserver/releases)
[![Binary size](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FMauricioPerera%2Fmicroserver%2Fbadges%2Fbinary-size.json)](https://github.com/MauricioPerera/microserver/actions/workflows/ci.yml)
[![Docker image size](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2FMauricioPerera%2Fmicroserver%2Fbadges%2Fdocker-size.json)](Dockerfile)

Servidor HTTP en Go que expone búsqueda semántica sobre SQLite (`sqlite-vec`), usando `embeddinggemma` vía Ollama para generar los embeddings.

Ver [CHANGELOG.md](CHANGELOG.md) para el historial de cambios.

## Requisitos

- Go 1.26+, con cgo habilitado (gcc/mingw en Windows).
- [Ollama](https://ollama.com) corriendo en `localhost:11434` con el modelo `embeddinggemma` descargado.
- Variables de entorno `AUTH_USERNAME` y `AUTH_PASSWORD` — **solo la primera vez** que corre contra una base de datos nueva (tabla de usuarios vacía): siembran el admin inicial. En arranques posteriores se ignoran — los usuarios se gestionan vía `/users`. El servidor sí falla al arrancar si la tabla de usuarios está vacía y estas variables no están seteadas (no hay forma de loguearse si eso pasa).
- **Build tag `sqlite_fts5` obligatorio** — sin él, SQLite no trae compilado el módulo FTS5 y **toda colección con vector falla al crearse** (no solo la búsqueda full-text), porque cada una crea automáticamente un índice FTS5 asociado. Ver sección Build.

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
AUTH_USERNAME=admin AUTH_PASSWORD=cambia-esto go run -tags sqlite_fts5 .
```

Esas credenciales solo importan la primera vez (siembran el admin inicial) — ver sección Auth y roles más abajo. `-tags sqlite_fts5` es obligatorio, no opcional — sin él cualquier colección con vector falla al crearse (ver sección Build).

Escucha en `:8080`. Crea/usa `vec.db` en el directorio actual. Corre en background: checkpoint del WAL cada 5 min, backup rotado (`backups/`, conserva 7) cada 1h. `Ctrl+C` hace un checkpoint y backup final antes de salir.

## Build

Las dependencias están vendorizadas (`vendor/`), así que `go build`/`go test` no necesitan red ni tocar el module cache — Go usa `vendor/` automáticamente si el directorio existe.

> **Si corrés `go mod vendor` de nuevo** (ej. al agregar una dependencia nueva), se rompe el build con `fatal error: sqlite3.h: No such file or directory`. Motivo: `sqlite-vec-go-bindings/cgo` necesita ese header pero no lo trae, así que hay una copia puesta a mano en `vendor/github.com/asg017/sqlite-vec-go-bindings/cgo/sqlite3.h` (el mismo amalgamado que ya trae `mattn/go-sqlite3`, solo renombrado) — `go mod vendor` regenera el directorio entero desde cero y la borra, porque no es parte real de ningún módulo. Arreglo: `cp vendor/github.com/mattn/go-sqlite3/sqlite3-binding.h vendor/github.com/asg017/sqlite-vec-go-bindings/cgo/sqlite3.h` después de cada `go mod vendor`.

**El tag `sqlite_fts5` es obligatorio**, no opcional — sin él el binario compila igual, pero cualquier `POST /collections` con `vector:true` falla en runtime con `no such module: fts5`. Seteá `GOFLAGS` una vez y todos los comandos de Go de abajo lo heredan sin repetirlo:

```bash
export GOFLAGS=-tags=sqlite_fts5   # Linux/macOS
$env:GOFLAGS="-tags=sqlite_fts5"   # PowerShell
```

```bash
go build -o microserver.exe .   # Windows
go build -o microserver .       # Linux/macOS
```

Requiere `CGO_ENABLED=1` (es el default si Go detecta un compilador C) y gcc/mingw disponible — el binario enlaza estáticamente SQLite + la extensión `sqlite-vec` vía cgo.

**Cross-compilation:** no funciona con solo `GOOS`/`GOARCH` como en un build 100% Go. Al usar cgo, compilar para un SO o arquitectura distinta a la máquina actual requiere un cross-compilador de C para el target (ej. `zig cc`, o un toolchain mingw/gcc específico). Lo más simple y confiable es compilar nativamente en cada plataforma destino.

Verificar el binario antes de desplegar (con `GOFLAGS` ya seteado como arriba):
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
go build -tags sqlite_fts5 -o microserver .
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

`AUTH_USERNAME`/`AUTH_PASSWORD` solo siembran el admin inicial en una base vacía (ver Auth y roles) — el contenedor sí falla al arrancar si no hay usuarios y no están seteadas. El volumen (`-v microserver-data:/data`) persiste `vec.db`, `vec.db-wal/-shm` y `backups/` entre recreaciones del contenedor, incluidos los usuarios creados; sin él, se pierde todo al hacer `docker rm`.

`-p 8080:8080` publica el puerto directo, sin TLS. Para TLS con reverse proxy en Docker, la forma correcta es **no** publicar el puerto de `microserver` al host — ponerlo en una red Docker interna y dejar que un contenedor de Caddy (con su puerto 443 sí publicado) lo alcance por nombre de servicio, no por loopback.

### Respaldo del dato entre despliegues

Antes de reemplazar el binario o la máquina, copiar `vec.db` y `backups/` — son el estado; el binario es solo código.

## Endpoints

Todas las respuestas son JSON. Los cuerpos de request van sin encabezado `Content-Type` estricto (curl con `-d` funciona tal cual).

`GET /health`, `GET /metrics` y `POST /login` son públicos. Todo lo demás requiere el header `Authorization: Bearer <token>` obtenido de `/login`. Los `GET` (lectura) valen para cualquier usuario autenticado; todo lo que escribe (`POST`/`PUT`/`DELETE`) y `/users` entero requieren rol `admin` — un `read-only` autenticado recibe `403`, no `401`.

### Referencia rápida

| Método | Ruta | Rol | Descripción |
|---|---|---|---|
| GET | `/health` | público | Chequeo de salud. |
| GET | `/metrics` | público | Métricas Prometheus. |
| POST | `/login` | público | Login, devuelve token. |
| POST | `/users` | admin | Crear usuario. |
| GET | `/users` | admin | Listar usuarios (sin hash). |
| DELETE | `/users/{username}` | admin | Borrar usuario (protege al último admin). |
| PUT | `/users/me/password` | cualquiera | Cambiar la propia contraseña. |
| POST | `/items` | admin | Insertar en la tabla fija `vec_items`. |
| GET | `/items` | cualquiera | Listar items de `vec_items`. |
| PUT | `/items/{id}` | admin | Reemplazar un item de `vec_items`. |
| DELETE | `/items/{id}` | admin | Borrar un item de `vec_items`. |
| GET | `/search` | cualquiera | Búsqueda semántica en `vec_items`. |
| POST | `/collections` | admin | Crear una colección (con o sin vector, con referencias opcionales). |
| GET | `/collections` | cualquiera | Listar colecciones. |
| DELETE | `/collections/{name}` | admin | Borrar una colección entera. |
| POST | `/collections/{name}/items` | admin | Insertar un item en una colección. |
| GET | `/collections/{name}/items` | cualquiera | Listar items — con filtro y orden opcionales. |
| GET | `/collections/{name}/items/{id}` | cualquiera | Traer un item por id. |
| PUT | `/collections/{name}/items/{id}` | admin | Reemplazar un item. |
| DELETE | `/collections/{name}/items/{id}` | admin | Borrar un item (respeta referencias `restrict`/`set_null`). |
| GET | `/collections/{name}/search` | cualquiera | Búsqueda semántica (solo colecciones con vector) — filtro y orden opcionales. |
| GET | `/collections/{name}/fulltext` | cualquiera | Búsqueda de texto completo FTS5 sobre `text` (solo colecciones con vector). |
| GET | `/collections/{name}/aggregate` | cualquiera | `count`/`sum`/`avg`/`min`/`max`, con `group_by` y filtro opcionales. |

"cualquiera" = cualquier usuario autenticado, admin o read-only.

Detalle completo de cada uno abajo.

### `GET /health`

Chequeo de salud. Público.

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### `POST /login`

Autentica contra la tabla de usuarios (no contra `AUTH_USERNAME`/`AUTH_PASSWORD` — esas solo siembran el admin inicial, ver Auth y roles abajo) y devuelve un token firmado (HMAC) que incluye el rol del usuario, válido por 1 hora. Público.

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

El token es stateless (sin sesión en el servidor) — se firma con una clave aleatoria generada en memoria al arrancar el proceso, así que **todos los tokens de todos los usuarios quedan inválidos si el servidor se reinicia**; hay que loguearse de nuevo. El rol viaja embebido en el token, así que si le cambiás el rol a alguien con un token vigente, ese token sigue reflejando el rol viejo hasta que expira (máx. 1h) — no hay revocación.

## Auth y roles

No hay un solo usuario compartido — hay una tabla de usuarios (`username`, contraseña hasheada con bcrypt, rol). Dos roles, parejos en toda la API, no por colección:
- **`admin`**: todo. Lectura, escritura, y gestión de usuarios.
- **`read-only`**: solo lectura (`GET /items`, `/search`, `/collections/*` de lectura, `/aggregate`, `/fulltext`). Cualquier escritura o `/users` devuelve `403` (no `401` — está autenticado, solo no autorizado).

`AUTH_USERNAME`/`AUTH_PASSWORD` siembran el primer admin **solo si la tabla de usuarios está vacía**. Después de eso se ignoran — gestioná usuarios con estos endpoints.

### `POST /users` — requiere rol admin

Crea un usuario. Contraseña mínimo 8 caracteres.

**Body:**
```json
{"username": "lector", "password": "unabuenacontrasena", "role": "read-only"}
```

**Respuesta:** `201 Created` con `{"username","role"}`. `409` si ya existe. `400` si el username es inválido (mismo patrón que nombres de colección: `^[a-zA-Z][a-zA-Z0-9_]{0,62}$`), el rol no es `admin`/`read-only`, o la contraseña es muy corta.

```bash
curl -X POST http://localhost:8080/users -H "Authorization: Bearer $TOKEN" -d '{"username":"lector","password":"unabuenacontrasena","role":"read-only"}'
```

### `GET /users` — requiere rol admin

Lista usuarios. Nunca incluye el hash de contraseña.

```bash
curl http://localhost:8080/users -H "Authorization: Bearer $TOKEN"
# [{"username":"admin","role":"admin"},{"username":"lector","role":"read-only"}]
```

### `DELETE /users/{username}` — requiere rol admin

Borra un usuario. `204`/`404`. Rechaza borrar el último admin que queda (`409`) — no hay superusuario ni forma de recuperarse de quedarse sin ningún admin.

```bash
curl -X DELETE http://localhost:8080/users/lector -H "Authorization: Bearer $TOKEN"
```

### `PUT /users/me/password` — cualquier usuario autenticado

Cambia la propia contraseña (no la de otro — el usuario objetivo sale del token, no de la URL). Requiere la contraseña actual.

**Body:**
```json
{"current_password": "unabuenacontrasena", "new_password": "otracontrasenamejor"}
```

**Respuesta:** `204 No Content`. `401` si `current_password` no coincide, `400` si `new_password` tiene menos de 8 caracteres. Los tokens ya emitidos con la contraseña vieja siguen válidos hasta que expiran (el token no incluye la contraseña, así que no hay nada que invalidar).

```bash
curl -X PUT http://localhost:8080/users/me/password -H "Authorization: Bearer $TOKEN" -d '{"current_password":"unabuenacontrasena","new_password":"otracontrasenamejor"}'
```

### `POST /items` — requiere rol admin

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

### `GET /items`

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

### `PUT /items/{id}` — requiere rol admin

Reemplaza el texto de un item existente (re-embebe y sobreescribe sus vectores).

**Body:**
```json
{"text": "un felino descansa sobre el mueble"}
```

**Respuesta:** `200 OK` con `{"id": N}`. `404` si el id no existe. `400` si falta `text` o el id no es entero.

```bash
curl -X PUT http://localhost:8080/items/123 -H "Authorization: Bearer $TOKEN" -d '{"text":"un felino descansa sobre el mueble"}'
```

### `DELETE /items/{id}` — requiere rol admin

Borra un item.

**Respuesta:** `204 No Content` si se borró. `404` si el id no existía. `400` si el id no es entero.

```bash
curl -X DELETE http://localhost:8080/items/123 -H "Authorization: Bearer $TOKEN"
```

### `GET /search`

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

## Colecciones

`vec_items` (los endpoints `/items` y `/search` de arriba) es una tabla fija con un esquema fijo. Las **colecciones** son tablas adicionales, creadas a pedido, cada una con su propio nombre y forma de datos — para no estar atado a un solo esquema.

Cada colección es o bien:
- **Sin vector**: solo `id` + un campo `data` con cualquier JSON que quieras (objetos anidados, arrays, lo que sea) — un almacén de documentos simple, sin búsqueda semántica.
- **Con vector**: además de `data`, tiene `text` (lo que se embebe y se busca) + los mismos dos vectores (float32 + binario) que `vec_items`. Mismo trade-off `rerank` que `/search`.

Las colecciones pueden **referenciarse entre sí** (ver `references` en `POST /collections` abajo) — un campo de `data` puede apuntar al id de un item en otra colección, validado en cada insert/update, con `restrict` o `set_null` al borrar el item referenciado.

**Lo que esto NO es**, para no generar expectativas de más: no hay joins (`GET /collections/{name}/items/{id}` es el único lookup puntual — resolver una referencia son N llamadas, una por cada id), el filtrado, el orden y las agregaciones son solo sobre campos de nivel superior de `data`, uno a la vez — nada de orden multi-columna ni `group_by` por más de un campo, y no hay transacciones atómicas entre colecciones.

Todos requieren auth; los de escritura además requieren rol admin.

### `POST /collections` — requiere rol admin

Crea una colección. `dimensions` es obligatorio (1–8192) si `vector` es `true`. `references` es opcional — declara campos de `data` que deben apuntar a un id existente en otra colección (la colección referenciada debe existir de antes).

**Body:**
```json
{
  "name": "publicaciones",
  "vector": false,
  "references": {
    "autor_id": {"collection": "autores", "on_delete": "restrict"},
    "categoria_id": {"collection": "categorias", "on_delete": "set_null"}
  }
}
```

`on_delete` (default `restrict` si se omite):
- `restrict`: no deja borrar el item referenciado mientras exista al menos un item apuntándolo (`409`).
- `set_null`: al borrar el item referenciado, pone ese campo en `null` en todos los items que lo referenciaban.

**Respuesta:** `201 Created` con el objeto de la colección. `409` si ya existe. `400` si el nombre es inválido (debe matchear `^[a-zA-Z][a-zA-Z0-9_]{0,62}$`), si `dimensions` es inválido, si algún `references[].collection` no existe todavía, o si `on_delete` no es `restrict`/`set_null`.

```bash
curl -X POST http://localhost:8080/collections -H "Authorization: Bearer $TOKEN" -d '{"name":"autores","vector":false}'
curl -X POST http://localhost:8080/collections -H "Authorization: Bearer $TOKEN" -d '{"name":"categorias","vector":false}'
curl -X POST http://localhost:8080/collections -H "Authorization: Bearer $TOKEN" -d '{"name":"publicaciones","vector":false,"references":{"autor_id":{"collection":"autores","on_delete":"restrict"},"categoria_id":{"collection":"categorias","on_delete":"set_null"}}}'
```

### `GET /collections`

Lista las colecciones existentes.

```bash
curl http://localhost:8080/collections -H "Authorization: Bearer $TOKEN"
```

### `DELETE /collections/{name}` — requiere rol admin

Borra la colección y todos sus items. `204` si se borró, `404` si no existía.

```bash
curl -X DELETE http://localhost:8080/collections/documentos -H "Authorization: Bearer $TOKEN"
```

### `POST /collections/{name}/items` — requiere rol admin

Inserta un item. `text` es obligatorio solo si la colección tiene vector (es lo que se embebe); `data` es siempre opcional, cualquier JSON.

**Body:**
```json
{"text": "el gato duerme en el sofá", "data": {"categoria": "animales"}, "id": 123}
```

**Respuesta:** `201 Created` con `{"id": N}`. `404` si la colección no existe. `400` si falta `text` en una colección con vector.

```bash
curl -X POST http://localhost:8080/collections/documentos/items -H "Authorization: Bearer $TOKEN" -d '{"text":"el gato duerme en el sofá","data":{"categoria":"animales"}}'
curl -X POST http://localhost:8080/collections/notas/items -H "Authorization: Bearer $TOKEN" -d '{"data":{"titulo":"compras","items":["leche","pan"]}}'
```

### `GET /collections/{name}/items`

Lista items (`limit`/`offset`, igual que `GET /items`). Cualquier otro query param filtra por un campo de nivel superior de `data`: `campo=valor` (equals) o `campo__op=valor` con `op` en `eq`, `ne`, `lt`, `lte`, `gt`, `gte`, `like`. Varios params se combinan con AND. Un item sin ese campo, o con el campo en `null`, no matchea nunca (comparación contra NULL en SQL).

`sort=campo` ordena ascendente por ese campo de `data` en vez del `id` por defecto; `sort=-campo` es descendente. Un solo campo, no hay orden multi-columna.

```bash
curl "http://localhost:8080/collections/productos/items?categoria=perifericos" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/items?precio__lt=50" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/items?categoria=perifericos&precio__gte=40" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/items?sort=-precio" -H "Authorization: Bearer $TOKEN"
```

**Errores:** `400` si el nombre de campo (filtro u orden) es inválido o el operador no existe.

### `GET /collections/{name}/items/{id}`

Trae un item por id — el único "lookup puntual" que existe (no hay joins: para resolver una referencia hay que llamar esto en la colección referenciada).

**Respuesta:** `200 OK` con el item. `404` si no existe.

```bash
curl http://localhost:8080/collections/autores/items/1 -H "Authorization: Bearer $TOKEN"
```

### `PUT /collections/{name}/items/{id}` — requiere rol admin

Reemplaza `text`/`data` de un item. Mismo requisito de `text` que el insert, y las referencias en `data` se validan igual que en el insert.

```bash
curl -X PUT http://localhost:8080/collections/notas/items/1 -H "Authorization: Bearer $TOKEN" -d '{"data":{"titulo":"compras","done":true}}'
```

### `DELETE /collections/{name}/items/{id}` — requiere rol admin

Borra un item. `204`/`404`. Si otras colecciones tienen items con referencia `restrict` apuntando a este, `409` (no borra nada). Con `set_null`, borra igual y pone `null` en el campo de cada item que lo referenciaba.

### `GET /collections/{name}/search`

Igual que `GET /search`, pero en una colección elegida. Solo válido si la colección tiene vector — `400` si no. Acepta los mismos filtros y `sort` que `GET .../items` (búsqueda híbrida: similitud semántica + condición sobre `data`, con orden final configurable).

```bash
curl "http://localhost:8080/collections/documentos/search?q=un+gato+durmiendo&limit=5" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/documentos/search?q=un+gato+durmiendo&limit=5&fuente=wiki" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/documentos/search?q=un+gato+durmiendo&limit=5&sort=-fecha" -H "Authorization: Bearer $TOKEN"
```

**Filtrar + buscar es best-effort, no exacto** — verificado empíricamente contra `sqlite-vec`: al combinar `MATCH` con una condición extra, `vec0` exige un `k = N` que limita cuántos candidatos por cercanía se escanean *antes* de aplicar el filtro, no al revés. Si el único item que matchea el filtro está semánticamente lejos de la consulta (fuera de esos N más cercanos), no aparece — aunque sea el único resultado válido. Se usa el mismo margen (`oversampleFactor`, 8x) que ya se usa para el rerank, pero no hay garantía de encontrar todo lo que matchea si el dato filtrado es raro y semánticamente distinto de la búsqueda.

**`sort` no cambia qué documentos se seleccionan, solo el orden final** — la selección de candidatos sigue gobernada por similitud vectorial (y los filtros, con el mismo trade-off de arriba); `sort` reordena el resultado ya elegido. También verificado empíricamente: `vec0` reconoce `ORDER BY distance` como parte especial de una consulta KNN — reemplazarlo por un campo de `data` dispara el mismo requisito de `k = N` que un filtro extra, aunque no haya ningún filtro.

### `GET /collections/{name}/fulltext`

Búsqueda de texto completo (FTS5) sobre el campo `text` — distinta de `GET .../search`, que busca por similitud semántica del embedding. Solo válido en colecciones con vector — `400` si no (las colecciones sin vector no tienen campo `text`, solo `data`). Acepta los mismos filtros y `sort` que `GET .../items`.

`q` se pasa tal cual como sintaxis de consulta FTS5, no se sanitiza a una frase plana: soporta `AND`/`OR`/`NOT`, `"frase exacta"`, prefijo con `*`, etc.

**Query params:**
| Param | Requerido | Descripción |
|---|---|---|
| `q` | sí | consulta en sintaxis FTS5 |
| `limit` | no | default 10 |
| `offset` | no | default 0 |

**Respuesta:** `200 OK`, ordenada por relevancia (`bm25`) salvo que se pase `sort`.

```bash
curl "http://localhost:8080/collections/documentos/fulltext?q=gato" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/documentos/fulltext?q=%22frase+exacta%22" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/documentos/fulltext?q=gato+AND+negro&categoria=animales" -H "Authorization: Bearer $TOKEN"
```

**Errores:** `400` si `q` falta, si la colección no tiene vector, o si `q` es sintaxis FTS5 inválida (ej. comillas sin cerrar) — cualquier error de ejecución de la consulta se trata como error del cliente, no del servidor, porque el único punto de falla ahí es la sintaxis que mandó el cliente.

### `GET /collections/{name}/aggregate`

Agregaciones sobre un campo de `data`, con `group_by` y filtros opcionales (misma sintaxis de filtro que `GET .../items`). No es específico de colecciones con vector — funciona igual en ambas.

**Query params:**
| Param | Requerido | Descripción |
|---|---|---|
| `op` | sí | `count`, `sum`, `avg`, `min`, `max` |
| `field` | solo si `op` ≠ `count` | campo de `data` a agregar. Con `count` es opcional: sin `field` cuenta todas las filas (`COUNT(*)`), con `field` cuenta solo las no-null |
| `group_by` | no | campo de `data` para agrupar |

**Respuesta:** `200 OK`, siempre un array — un elemento si no hay `group_by`, uno por grupo si lo hay. `value` es `null` (no `0`) cuando el agregado no tiene filas sobre las que calcular (ej. `sum`/`avg`/`min`/`max` sin matches — así se distingue "no hay datos" de "los datos suman cero").

```bash
curl "http://localhost:8080/collections/productos/aggregate?op=count" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/aggregate?op=sum&field=precio" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/aggregate?op=avg&field=precio&categoria=perifericos" -H "Authorization: Bearer $TOKEN"
curl "http://localhost:8080/collections/productos/aggregate?op=sum&field=precio&group_by=categoria" -H "Authorization: Bearer $TOKEN"
```

**Errores:** `400` si `op` es inválido, si falta `field` para un op que lo requiere, o si algún nombre de campo/filtro es inválido. `404` si la colección no existe.

## Límites de la API

- **Tamaño de body**: máximo 1 MiB por request. Pasarse devuelve `413 Payload Too Large` con el límite exacto en el mensaje.
- **Rate limit general**: 20 req/s por IP, ráfaga de 40, sobre toda la API salvo `/login`. `429 Too Many Requests` con header `Retry-After` al pasarse.
- **Rate limit de `/login`**: más estricto, 5 intentos/minuto por IP (ráfaga de 5) — no hay bloqueo de cuenta de otra forma, así que esto es lo único que frena fuerza bruta sobre la contraseña.

**La IP que ve el rate limiter es la que llega por TCP a este proceso** — si corre detrás de Caddy (ver TLS arriba) sin configurar `X-Forwarded-For`, esa IP es siempre la del proxy, no la del visitante real. En ese despliegue el límite pasa a ser efectivamente global (protege el proceso/Ollama de saturarse), no por-cliente. No se confía en `X-Forwarded-For` por defecto — un cliente directo podría falsificarlo para eludir su propio límite.

## Observabilidad

### Logs

Estructurados en JSON (`log/slog`, stdlib — sin dependencia nueva) a stdout. Cada request termina con una línea `msg=request`: `method`, `path` (el patrón de ruta, ej. `/collections/{name}/items/{id}` — no la URL cruda con ids reales), `status`, `duration_ms`. Los errores (`status >= 400`) van a nivel `INFO`; los éxitos a nivel `DEBUG`, invisibles con el nivel por defecto — para ver tráfico exitoso hay que subir el nivel del handler en [main.go](main.go).

### `GET /metrics`

Métricas en formato de exposición de Prometheus (texto plano, sin la librería `client_golang` — se genera a mano, es el único consumidor que necesita este proyecto hoy). Público, sin auth, igual que `/health` — si te preocupa exponer conteos/latencias por ruta, restringilo a nivel de red o del reverse proxy, no acá.

```bash
curl http://localhost:8080/metrics
```

```
http_requests_total{method="GET",path="/items",status="200"} 2
http_request_duration_seconds_sum{method="POST",path="/items"} 0.338776
http_request_duration_seconds_count{method="POST",path="/items"} 1
```

Etiquetado por **patrón de ruta**, no por URL cruda — `GET /collections/{name}/items/{id}` es una sola serie sin importar cuántas colecciones o ids distintos se pidan. Etiquetar por URL cruda haría explotar la cardinalidad de métricas con una serie nueva por cada combinación única de colección+id jamás solicitada.

## Notas de arquitectura

- **Escrituras**: un único pool de conexión (`SetMaxOpenConns(1)`) serializa los inserts/updates/deletes — SQLite nunca permite escritores concurrentes reales; esto evita errores de "database is locked" en vez de intentar paralelizar lo que no se puede.
- **Lecturas**: pool separado con varias conexiones, no bloqueado por el escritor (modo WAL).
- **UPDATE**: implementado como `DELETE` + `INSERT` en una transacción, no como `UPDATE` SQL directo — `vec0` (la virtual table de `sqlite-vec`) no reconoce correctamente el tipo binario cuantizado en su path de `UPDATE`. Aplica también a colecciones con vector.
- **Colecciones y nombres de tabla**: SQLite no permite parametrizar identificadores (nombres de tabla) en una consulta preparada, así que el nombre de colección se valida contra `^[a-zA-Z][a-zA-Z0-9_]{0,62}$` antes de interpolarlo en cualquier DDL — esa whitelist es toda la defensa contra inyección vía nombre de colección, no hay otra capa.
- **Índice full-text (FTS5)**: cada colección con vector tiene una tabla FTS5 aparte (`coll_<nombre>_fts`), sincronizada a mano en Go en cada insert/update/delete — no con triggers de SQL sobre la tabla principal, porque esa tabla principal es una virtual table de `vec0` y no verificamos si los triggers funcionan bien ahí. `update`/`delete` la mantienen sincronizada dentro de la misma transacción que toca la tabla principal, así nunca quedan desincronizadas aunque algo falle a mitad de camino.
- **bcrypt (`golang.org/x/crypto`)**: única dependencia externa nueva agregada deliberadamente a un proyecto que por lo demás evita dependencias de terceros (badges self-hosted, rate limiter hecho a mano, etc.). Hashear contraseñas a mano es un riesgo de seguridad real, no una preferencia estética — a diferencia del rate limiter (un algoritmo simple y bien entendido), esto sí amerita usar una librería madura y auditada.
- **Rol embebido en el token, no en una tabla de sesiones**: el login mete el rol del usuario directo en el payload firmado (`username:role:expiry`). Mantiene el diseño stateless existente (sin sesiones en el servidor), pero significa que cambiarle el rol a alguien no afecta sus tokens ya emitidos hasta que expiran (máx. 1h) — no hay revocación.
- **Protección del último admin**: `deleteUser` cuenta cuántos admins quedan antes de borrar uno; si es el último, rechaza con `409`. No hay superusuario de emergencia ni forma de recuperar acceso si te quedás sin ningún admin — por diseño, dado que tampoco había uno antes de esta feature.
