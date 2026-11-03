# Публикация в Docker Hub

Образ опубликован в [Docker Hub](https://hub.docker.com/r/exintegra/ci-variables) для `linux/amd64` и `linux/arm64`. Проверены теги `1.0.0` и `latest`: оба указывают на один выпуск. Ниже описаны запуск, локальная сборка и публикация следующих версий.

Digest выпуска `1.0.0`: `sha256:027552c1ee0c2eb678771fee90b65a8eb9be07f67452ede71363f5a95d78b0c7`.

## Быстрый запуск

Нужен запущенный Docker Desktop с Linux-контейнерами. В папке с `compose.yaml` выполните:

```powershell
docker compose up -d
docker compose ps
```

Откройте [http://localhost:8081](http://localhost:8081) и введите GitLab token в форме входа. Токен не передаётся Docker и не записывается в конфигурацию. Выбранные проекты и группы сохраняются в томе `ci-variables-data`. Порт `8081` позволяет сохранить отдельный запуск Go-приложения на `8080`.

Для запуска достаточно одного `compose.yaml`: его можно скопировать в пустую папку. Образ скачивается автоматически, `.env` необязателен. Если нужны другой GitLab или порт, скопируйте `.env.example` в `.env` и измените `GITLAB_URL` или `APP_PORT`. При явно заданном `APP_ORIGIN` он должен точно совпадать с адресом приложения в браузере.

```powershell
# Последние сообщения и наблюдение за работой; Ctrl+C завершает просмотр.
docker compose logs --tail=100 -f
# Остановить и затем запустить существующий контейнер.
docker compose stop
docker compose start
# Удалить контейнер и сеть, сохранив настройки в томе.
docker compose down
# Снова запустить с сохранёнными настройками.
docker compose up -d
```

Чтобы обновить приложение, измените тег `IMAGE` в `.env` на опубликованную новую версию и выполните `docker compose pull`, затем `docker compose up -d`. При использовании тега `latest` эти команды скачивают текущий выпуск. После перезапуска потребуется повторный вход.

### Переход с прежнего запуска

Если приложение уже работает через `docker run` на порте `8081`, сначала остановите его: `docker stop ci-variables`. Старый контейнер и его данные при этом сохраняются. Compose создаёт контейнер с другим именем. Том `ci-variables-data` из последней команды запуска будет использован автоматически.

Если прежний запуск использовал том `tracking-data` или `<имя-проекта>_tracking-data`, укажите его точное имя в `.env`: `TRACKING_VOLUME=tracking-data`. Список существующих томов можно посмотреть командой `docker volume ls`. Это подключает прежние настройки вместо создания пустого тома. Настройки отдельного Go-процесса в `data/tracking.json` автоматически в Docker не переносятся.

## Что входит в образ

В образ попадают только Go-приложение и встроенные ресурсы интерфейса. `.dockerignore` разрешает сборке читать только `go.mod`, `cmd/` и `internal/`: настройки, данные отслеживания, журналы, скриншоты и локальные исполняемые файлы не передаются в контекст сборки. Токены GitLab вводятся после запуска в форме входа.

Приложение работает от пользователя `10001:10001`. Корневая файловая система в Compose доступна только для чтения; каталог `/data` хранится в отдельном томе. Там находятся только настройки выбранных проектов и групп, без токенов и значений переменных. Том сохраняется при обычном пересоздании контейнера. Команда `docker compose down -v` удаляет том вместе с этими настройками.

## Локальная проверка

Нужны работающий Docker с Linux-контейнерами и Buildx. Проверьте установку:

```powershell
docker version
docker buildx version
docker compose config --quiet
docker compose -f compose.yaml -f compose.build.yaml up --build -d
docker compose ps
```

Откройте [http://localhost:8081](http://localhost:8081). Для демонстрации без настоящего GitLab можно перед запуском задать `$env:APP_DEMO = "true"`. Проверка `/healthz` встроена в образ; она проверяет HTTP-сервер приложения и не обращается к GitLab.

## Подготовка аккаунта

1. Для этого проекта используется аккаунт Docker Hub `exintegra` и репозиторий `ci-variables`.
2. Создайте Docker Hub access token с правом записи в этот репозиторий.
3. Скопируйте `.env.example` в `.env`. Для этого проекта namespace - `exintegra`, имя образа - `ci-variables`. `.env` содержит только несекретные настройки.
4. Выполните `docker login --username exintegra` и введите Docker Hub token в интерактивном запросе пароля. Не указывайте токен аргументом команды и не кладите его в `.env`.

GitLab token и Docker Hub token выполняют разные задачи: первый вводится в веб-приложении, второй используется только Docker при публикации.

## Сборка без публикации

Сначала можно посмотреть план без запуска Docker и записи файлов:

```powershell
./scripts/publish-image.ps1 -Version 1.0.0 -DockerHubUsername exintegra -WhatIf
```

Для сборки двух архитектур в локальный OCI-архив выполните ту же команду без `-WhatIf`:

```powershell
./scripts/publish-image.ps1 -Version 1.0.0 -DockerHubUsername exintegra
```

Результат - `artifacts/ci-variables-1.0.0.oci.tar`. В Docker Hub ничего не отправляется. Сценарий создаёт отдельный сборщик `gitlabvars-builder` с драйвером `docker-container`; текущий сборщик Docker по умолчанию не меняется. Уже существующий сборщик этого типа можно выбрать параметром `-Builder`.

Docker Desktop обычно уже поддерживает нужную эмуляцию ARM64. Для проверки сборки только одной архитектуры можно передать `-Platforms linux/amd64` или `-Platforms linux/arm64`.

## Публикация

После проверки исходников, версии и namespace явно добавьте `-Push`:

```powershell
./scripts/publish-image.ps1 -Version 1.0.0 -DockerHubUsername exintegra -Push
docker buildx imagetools inspect exintegra/ci-variables:1.0.0
```

Стабильная версия получает теги `1.0.0` и `latest`. Предварительная версия, например `1.1.0-rc.1`, получает только собственный тег. Сценарий не повторяет публикацию автоматически при ошибке. Повторное использование существующего тега в Docker Hub может заменить его содержимое, поэтому для нового выпуска задавайте новую версию.

Команда `imagetools inspect` должна показать варианты `linux/amd64` и `linux/arm64`. Поле `Digest` - идентификатор опубликованного содержимого. Для закреплённого развёртывания можно использовать `IMAGE=exintegra/ci-variables@sha256:...` с реальным digest из результата, вместо изменяемого тега.

## Запуск опубликованного образа

По умолчанию основной Compose-файл использует выпуск `1.0.0`. Необязательный `.env` позволяет изменить версию и настройки:

```dotenv
IMAGE=exintegra/ci-variables:1.0.0
APP_PORT=8081
GITLAB_URL=https://gitlab.com
APP_DEMO=false
SESSION_TTL=1h
```

Затем:

```powershell
docker compose config --quiet
docker compose pull
docker compose up -d
docker compose ps
```

`compose.yaml` скачивает образ и не собирает исходники. `compose.release.yaml` оставлен как совместимая конфигурация с теми же настройками. Порт привязан к `127.0.0.1`; для работы через домен используйте HTTPS reverse proxy и точный внешний адрес в `APP_ORIGIN`. Конфигурация локальной сборки использует тот же Compose-проект: это альтернативный способ запуска, а не вторая копия приложения.

## Необязательная публикация через GitHub Actions

Если исходники будут размещены на GitHub, готов workflow `.github/workflows/docker-publish.yml`:

- В настройках репозитория создайте Actions secrets `DOCKERHUB_USERNAME` и `DOCKERHUB_TOKEN`.
- При нестандартном имени образа задайте Actions variable `IMAGE_NAME`; по умолчанию используется `ci-variables`.
- Ручной запуск с номером версии и выключенным `publish` проверяет сборку без входа в Docker Hub и публикации.
- Ручной запуск с включённым `publish` либо отправка Git-тега вида `v1.0.0` публикует образ после `go test` и `go vet`.

Workflow не запускается для pull request, не передаёт registry-токен в аргументы Dockerfile и имеет только `contents: read` для GitHub. Создание GitHub-репозитория, настройка secrets и запуск workflow не выполнялись автоматически.

Официальная документация: [multi-platform сборка](https://docs.docker.com/build/building/multi-platform/), [Docker GitHub Actions](https://docs.docker.com/build/ci/github-actions/multi-platform/), [теги и метаданные](https://docs.docker.com/build/ci/github-actions/manage-tags-labels/).
