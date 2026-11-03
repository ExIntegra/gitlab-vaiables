# Публикация исходников в GitHub

Папка `ci-variables` содержит готовый проект: Go-сервер, интерфейс, тесты, Terraform-шаблоны, Dockerfile, Compose и инструкции.

Создайте пустой репозиторий `ci-variables` в своём аккаунте GitHub, затем выполните команды в этой папке:

```powershell
git init -b main
git add .
git diff --cached --stat
git commit -m "Initial CI Variables application"
git remote add origin https://github.com/YOUR_USERNAME/ci-variables.git
git push -u origin main
```

Замените `YOUR_USERNAME` своим именем аккаунта. Токены вводятся средствами GitHub или менеджера учётных данных, а не в URL репозитория.

GitHub Actions автоматически проверяет Go-тесты, vet и сборку при push и pull request. Публикация Docker-образа выполняется отдельным workflow `docker-publish.yml`. Для неё задайте secrets `DOCKERHUB_USERNAME` и `DOCKERHUB_TOKEN`; обычная проверка Go этих секретов не требует.

Опубликованный Docker-образ `1.0.0` предшествует генератору инфраструктуры. Для проверки текущего кода используйте:

```powershell
docker compose -f compose.yaml -f compose.build.yaml up --build -d
```

Не добавляйте `.env`, каталог `data`, Terraform state, локальные исполняемые файлы и архивы. Правила исключения находятся в `.gitignore`.

Лицензия распространения не назначена: перед публичной публикацией автор может выбрать её и добавить файл LICENSE.
