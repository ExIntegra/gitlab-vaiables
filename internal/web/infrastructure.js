"use strict";

window.createInfrastructureEditor = function ({ state, api, el, icon, requestScope, sessionError, toast }) {
  const $ = (id) => document.getElementById(id);
  let groups = [], result = null, revision = 0, timer = null, fieldSequence = 0;
  const blankProject = () => ({ name: "", description: "", visibility_level: "private", initialize_with_readme: true, default_branch: "main", merge_method: "merge", protected_branches: [] });
  const blankGroup = () => ({ name: "", parent_group: "", description: "", visibility_level: "private", members: [], projects: [] });
  const visibility = [["private", "Приватный"], ["internal", "Внутренний"], ["public", "Публичный"]];
  const access = [["no one", "Никто"], ["maintainer", "Maintainer"], ["developer", "Developer и выше"]];
  const roles = [["developer", "Developer"], ["maintainer", "Maintainer"], ["reporter", "Reporter"], ["guest", "Guest"], ["owner", "Owner"]];

  function button(label, handler, primary = false) {
    const node = el("button", `button button-${primary ? "primary" : "secondary"}`); node.type = "button";
    node.append(icon("plus"), document.createTextNode(label)); node.addEventListener("click", handler); return node;
  }
  function remove(label, handler, confirm = false) {
    const node = el("button", "icon-button"); node.type = "button"; node.title = label; node.setAttribute("aria-label", label); node.append(icon("trash"));
    let armed = false;
    node.addEventListener("click", () => {
      if (confirm && !armed) { armed = true; node.className = "button button-danger"; node.textContent = "Удалить группу?"; node.setAttribute("aria-label", "Подтвердить удаление группы из черновика"); return; }
      handler(); render(); changed();
    });
    return node;
  }
  function field(label, object, key, options = {}) {
    const wrap = el("div", `infrastructure-field${options.wide ? " infrastructure-wide" : ""}`), id = `infra-field-${++fieldSequence}`;
    const caption = el("label", "", label); caption.htmlFor = id;
    const input = document.createElement(options.choices ? "select" : "input"); input.id = id;
    if (options.choices) for (const [value, text] of options.choices) input.append(new Option(text, value));
    else { input.type = "text"; input.autocomplete = "off"; input.spellcheck = false; input.maxLength = options.maxLength || 100; if (options.placeholder) input.placeholder = options.placeholder; }
    input.value = object[key];
    input.addEventListener(options.choices ? "change" : "input", () => { object[key] = input.value; options.onChange?.(input); changed(); });
    wrap.append(caption, input); if (options.help) wrap.append(el("p", "field-help", options.help)); return wrap;
  }
  function checkbox(label, object, key, onChange) {
    const wrap = el("label", "infrastructure-checkbox"), input = document.createElement("input"); input.type = "checkbox"; input.checked = object[key];
    input.addEventListener("change", () => { object[key] = input.checked; onChange?.(); changed(); }); wrap.append(input, document.createTextNode(label)); return wrap;
  }
  function section(label, addLabel, add) {
    const node = el("section", "infrastructure-section"), heading = el("div", "infrastructure-section-title"); heading.append(el("h3", "", label), button(addLabel, () => { add(); render(); changed(); })); node.append(heading); return node;
  }
  function renderProject(project, group, index) {
    const node = el("article", "infrastructure-project"), header = el("div", "infrastructure-project-title"), title = el("strong", "", project.name || `Новый проект ${index + 1}`);
    header.append(el("span", "badge", "P"), title, remove("Удалить проект из черновика", () => group.projects.splice(index, 1))); node.append(header);
    const fields = el("div", "infrastructure-fields");
    fields.append(field("Имя проекта", project, "name", { placeholder: "payments-api", onChange: () => title.textContent = project.name || "Новый проект" }), field("Видимость проекта", project, "visibility_level", { choices: visibility }), field("Описание проекта", project, "description", { wide: true, maxLength: 2000 })); node.append(fields);
    const policy = el("details", "infrastructure-policy"); policy.append(el("summary", "", "Редактировать политику веток"));
    const branchFields = el("div", "infrastructure-fields");
    const defaultField = field("Основная ветка", project, "default_branch", { placeholder: "main", maxLength: 255 });
    defaultField.querySelector("input").disabled = !project.initialize_with_readme;
    branchFields.append(defaultField, field("Метод слияния", project, "merge_method", { choices: [["merge", "Merge commit"], ["rebase_merge", "Merge commit + rebase"], ["ff", "Fast-forward"]] }));
    policy.append(branchFields, checkbox("Создать репозиторий с README", project, "initialize_with_readme", () => defaultField.querySelector("input").disabled = !project.initialize_with_readme), el("p", "field-help", "Правила защищают ветки и шаблоны, но не создают дополнительные ветки."));
    const rules = el("div", "infrastructure-rules");
    project.protected_branches.forEach((rule, ruleIndex) => {
      const row = el("div", "infrastructure-rule"), heading = el("div", "infrastructure-rule-heading"); heading.append(el("span", "", `Защита ветки ${ruleIndex + 1}`), remove("Удалить правило ветки", () => project.protected_branches.splice(ruleIndex, 1)));
      const ruleFields = el("div", "infrastructure-fields"); ruleFields.append(field("Ветка или шаблон", rule, "name", { wide: true, placeholder: "main или release/*", maxLength: 255 }), field("Кто может push", rule, "push_access_level", { choices: access }), field("Кто может merge", rule, "merge_access_level", { choices: access }));
      row.append(heading, ruleFields, checkbox("Разрешить force push", rule, "allow_force_push")); rules.append(row);
    });
    policy.append(rules, button("Добавить правило защиты", () => { if (project.protected_branches.length >= 20) return toast("До 20 правил на проект."); project.protected_branches.push({ name: project.default_branch || "main", push_access_level: "no one", merge_access_level: "maintainer", allow_force_push: false }); render(); changed(); }));
    // Keep the policy open after adding a rule or editing existing rules.
    policy.open = project.protected_branches.length > 0; node.append(policy); return node;
  }
  function render() {
    const fragment = document.createDocumentFragment();
    groups.forEach((group, index) => {
      const card = el("article", "infrastructure-card"), heading = el("div", "infrastructure-card-heading"), title = el("strong", "", group.name || `Новая группа ${index + 1}`);
      heading.append(el("span", "badge", "G"), title, remove("Удалить группу из черновика", () => groups.splice(index, 1), group.projects.length > 0 || group.members.length > 0));
      const body = el("div", "infrastructure-card-body"), fields = el("div", "infrastructure-fields");
      fields.append(field("Имя группы", group, "name", { placeholder: "backend", onChange: () => title.textContent = group.name || "Новая группа" }), field("Родительская группа", group, "parent_group", { placeholder: "company/platform", maxLength: 500, help: "Полный путь существующей группы. Можно оставить пустым." }), field("Описание группы", group, "description", { maxLength: 2000 }), field("Видимость группы", group, "visibility_level", { choices: visibility }));
      body.append(fields);
      const members = section("Участники группы", "Добавить участника", () => { if (group.members.length >= 200) return toast("До 200 участников на группу."); group.members.push({ username: "", role: "developer" }); });
      if (!group.members.length) members.append(el("p", "infrastructure-empty-row", "Участники пока не добавлены."));
      group.members.forEach((member, memberIndex) => { const row = el("div", "infrastructure-member"); row.append(field("GitLab username", member, "username", { placeholder: "ivan", maxLength: 255 }), field("Роль", member, "role", { choices: roles }), remove("Удалить участника из черновика", () => group.members.splice(memberIndex, 1))); members.append(row); });
      const projects = section("Проекты", "Добавить проект", () => { if (group.projects.length >= 100) return toast("До 100 проектов на группу."); group.projects.push(blankProject()); });
      if (!group.projects.length) projects.append(el("p", "infrastructure-empty-row", "Нажмите плюс, чтобы добавить проект."));
      group.projects.forEach((project, projectIndex) => projects.append(renderProject(project, group, projectIndex)));
      body.append(members, projects); card.append(heading, body); fragment.append(card);
    });
    $("infrastructure-groups").replaceChildren(fragment); $("infrastructure-empty").hidden = groups.length > 0;
  }
  function downloads(enabled) { for (const id of ["infrastructure-copy", "infrastructure-download", "infrastructure-bundle"]) $(id).disabled = !enabled; }
  function output() {
    const terrakube = $("infrastructure-format").value === "terrakube";
    $("infrastructure-json").textContent = result ? (terrakube ? result.terrakube_value : result.tfvars_json) : "{}";
  }
  function changed() {
    revision++; clearTimeout(timer); state.requests.get("infrastructure")?.abort(); result = null; downloads(false); output();
    $("infrastructure-error").hidden = true; $("infrastructure-branches-help").hidden = true;
    const projects = groups.reduce((sum, group) => sum + group.projects.length, 0), members = groups.reduce((sum, group) => sum + group.members.length, 0);
    $("infrastructure-summary").textContent = groups.length ? `Групп: ${groups.length} · проектов: ${projects} · участников: ${members}` : "Черновик пуст";
    $("infrastructure-status").textContent = groups.length ? "Проверяем конфигурацию…" : "Добавьте группу для генерации.";
    if (groups.length && state.session) timer = setTimeout(generate, 450);
  }
  async function generate() {
    const currentRevision = revision, scope = requestScope("infrastructure");
    try {
      const response = await api("/api/infrastructure/generate", { method: "POST", body: JSON.stringify({ groups }), signal: scope.controller.signal });
      if (!scope.current() || revision !== currentRevision) return;
      result = response; output(); downloads(true); $("infrastructure-error").hidden = true;
      $("infrastructure-status").textContent = `Готово · групп: ${result.groups} · проектов: ${result.projects} · правил веток: ${result.branch_rules}`;
      $("infrastructure-branches-help").hidden = result.branch_rules === 0;
    } catch (error) {
      if (error.name === "AbortError" || !scope.current() || revision !== currentRevision || sessionError(error)) return;
      result = null; downloads(false); output(); $("infrastructure-status").textContent = "Заполните поля, чтобы получить конфигурацию.";
      $("infrastructure-error").textContent = error.message; $("infrastructure-error").hidden = false;
    }
  }
  function save(data, name, type) {
    const blob = new Blob([data], { type }), url = URL.createObjectURL(blob), link = document.createElement("a");
    link.href = url; link.download = name; document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  function replaceDraft(next) {
    // Keep one in-memory undo until the user accepts the replacement.
    if (!groups.length) { groups = next; render(); changed(); return; }
    const previous = groups; groups = next; render(); changed();
    const undo = el("button", "text-button", "Отменить замену черновика"); undo.type = "button";
    undo.addEventListener("click", () => { groups = previous; render(); changed(); undo.remove(); }); $("infrastructure-groups").prepend(undo);
  }
  function object(value, allowed, name) {
    if (!value || typeof value !== "object" || Array.isArray(value) || Object.keys(value).some((key) => !allowed.includes(key))) throw new Error(`Неизвестный формат: ${name}.`);
    for (const [key, item] of Object.entries(value)) {
      if (["description", "visibility_level", "default_branch", "merge_method", "push_access_level", "merge_access_level"].includes(key) && typeof item !== "string") throw new Error(`Поле ${key} должно быть строкой: ${name}.`);
      if (key === "parent_group" && item !== null && typeof item !== "string") throw new Error("parent_group должен содержать путь группы.");
      if (["initialize_with_readme", "allow_force_push"].includes(key) && typeof item !== "boolean") throw new Error(`Поле ${key} должно быть true или false.`);
    }
    return value;
  }
  function importDraft(parsed) {
    let source = parsed;
    if (parsed && Object.hasOwn(parsed, "infrastructure")) source = object(parsed, ["infrastructure"], "корень JSON").infrastructure;
    if (!source || typeof source !== "object" || Array.isArray(source) || Object.keys(source).length > 25) throw new Error("Ожидается объект infrastructure, до 25 групп.");
    return Object.entries(source).map(([name, value]) => {
      const group = object(value, ["parent_group", "description", "visibility_level", "members", "projects"], `группа ${name}`);
      const members = group.members ?? {}, projects = group.projects ?? {};
      if (typeof members !== "object" || !members || Array.isArray(members) || typeof projects !== "object" || !projects || Array.isArray(projects)) throw new Error("members и projects должны быть объектами.");
      if (Object.keys(members).length > 200 || Object.keys(projects).length > 100 || Object.values(members).some((role) => typeof role !== "string")) throw new Error("В группе допускается до 200 участников с текстовыми ролями и до 100 проектов.");
      return { ...blankGroup(), ...group, name, parent_group: group.parent_group ?? "", members: Object.entries(members).map(([username, role]) => ({ username, role })), projects: Object.entries(projects).map(([projectName, projectValue]) => {
        const project = object(projectValue, ["description", "visibility_level", "initialize_with_readme", "default_branch", "merge_method", "protected_branches"], `проект ${projectName}`), branches = project.protected_branches ?? {};
        if (typeof branches !== "object" || !branches || Array.isArray(branches)) throw new Error("protected_branches должен быть объектом.");
        if (Object.keys(branches).length > 20) throw new Error("В проекте допускается до 20 правил защиты веток.");
        return { ...blankProject(), ...project, name: projectName, protected_branches: Object.entries(branches).map(([branchName, rule]) => ({ push_access_level: "no one", merge_access_level: "maintainer", allow_force_push: false, ...object(rule, ["push_access_level", "merge_access_level", "allow_force_push"], "правило ветки"), name: branchName })) };
      }) };
    });
  }
  $("infrastructure-add-group").addEventListener("click", () => { if (groups.length >= 25) return toast("В одном комплекте до 25 групп."); groups.push(blankGroup()); render(); changed(); $("infrastructure-groups").lastElementChild?.querySelector("input")?.focus(); });
  $("infrastructure-format").addEventListener("change", output);
  $("infrastructure-copy").addEventListener("click", async () => {
    if (!result) return;
    try { await navigator.clipboard.writeText($("infrastructure-json").textContent); toast("Конфигурация скопирована"); }
    catch { const range = document.createRange(); range.selectNodeContents($("infrastructure-json")); const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range); toast("Текст выделен. Нажмите Ctrl+C для копирования."); }
  });
  $("infrastructure-download").addEventListener("click", () => { if (result) save($("infrastructure-json").textContent, $("infrastructure-format").value === "terrakube" ? "infrastructure.terrakube.json" : "terraform.tfvars.json", "application/json;charset=utf-8"); });
  $("infrastructure-bundle").addEventListener("click", () => { if (result) save(Uint8Array.from(atob(result.archive_base64), (character) => character.charCodeAt(0)), "gitlab-infrastructure.zip", "application/zip"); });
  $("infrastructure-example").addEventListener("click", () => replaceDraft([{ ...blankGroup(), name: "backend", parent_group: "company", description: "Команда backend", members: [{ username: "ivan", role: "maintainer" }, { username: "anna", role: "developer" }], projects: [{ ...blankProject(), name: "api", description: "Backend API", protected_branches: [{ name: "main", push_access_level: "no one", merge_access_level: "maintainer", allow_force_push: false }] }, { ...blankProject(), name: "database", description: "Миграции базы данных" }] }]));
  $("infrastructure-import").addEventListener("click", () => $("infrastructure-import-file").click());
  $("infrastructure-import-file").addEventListener("change", async (event) => {
    const file = event.target.files[0], session = state.session; event.target.value = ""; if (!file) return;
    try { if (file.size > 512 * 1024) throw new Error("JSON должен быть не больше 512 КБ."); const next = importDraft(JSON.parse(await file.text())); if (session !== state.session) return; replaceDraft(next); }
    catch (error) { toast(error instanceof SyntaxError ? "Не удалось прочитать JSON. Проверьте синтаксис файла." : error.message); }
  });
  render(); output();
  return { clear() { clearTimeout(timer); groups = []; revision++; result = null; render(); changed(); } };
};
