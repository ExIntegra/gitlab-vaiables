"use strict";
(() => {
  const $ = (id) => document.getElementById(id);
  const state = {
    config: {}, session: null, projects: [], catalog: new Map(), groups: [], groupTree: null,
    projectPage: 0, groupPage: 0, projectQuery: "", active: null, variables: [],
    requests: new Map(), sequences: new Map(), revealRequests: new Set(), reveals: new Set(), secretEpoch: 0,
    edit: null, deletion: null, creation: null, busy: false, editReloading: false, loginPending: false,
    projectSearchTimer: null, toastTimer: null,
  };
  class APIError extends Error { constructor(message, status, code) { super(message); this.status = status; this.code = code; } }
  const secret = (variable) => !!(variable.secret || variable.masked || variable.hidden);
  const resourcePath = (resource) => `/api/${resource.kind}/${resource.id}/variables`;
  const variablePath = (resource, variable) => `${resourcePath(resource)}/${encodeURIComponent(variable.key)}`;
  const resourceName = (resource) => resource?.path_with_namespace || resource?.full_path || resource?.name || "";
  const resourceID = (resource) => resource ? `${resource.kind}:${resource.id}` : "";
  const variableID = (variable) => `${variable.key}\u0000${variable.environment_scope}`;

  async function api(path, options = {}) {
    const headers = { Accept: "application/json", ...options.headers };
    if (options.body !== undefined) headers["Content-Type"] = "application/json";
    if (options.method && options.method !== "GET" && state.session) headers["X-CSRF-Token"] = state.session.csrf_token;
    let response;
    try { response = await fetch(path, { ...options, headers, credentials: "same-origin", mode: "same-origin", cache: "no-store" }); }
    catch (error) { if (error.name === "AbortError") throw error; throw new APIError("Не удалось связаться с приложением. Проверьте, запущен ли сервер, и повторите запрос.", 0, "network_error"); }
    let data;
    if (response.status !== 204) {
      try { data = await response.json(); }
      catch { throw new APIError("Сервер вернул неожиданный ответ. Повторите запрос.", response.status, "invalid_response"); }
    }
    if (!response.ok) {
      let message = data?.error?.message || "Не удалось выполнить запрос.";
      if (response.status === 429) { const seconds = Number(response.headers.get("Retry-After")); if (seconds > 0 && seconds <= 3600) message += ` Повторите через ${Math.ceil(seconds)} сек.`; }
      throw new APIError(message, response.status, data?.error?.code);
    }
    return data;
  }
  function el(tag, className, text) { const node = document.createElement(tag); if (className) node.className = className; if (text !== undefined) node.textContent = text; return node; }
  function icon(name) { const node = document.createElementNS("http://www.w3.org/2000/svg", "svg"); node.classList.add("icon"); node.setAttribute("aria-hidden", "true"); const use = document.createElementNS("http://www.w3.org/2000/svg", "use"); use.setAttribute("href", `#icon-${name}`); node.append(use); return node; }
  function action(label, symbol, handler, className = "") { const button = el("button", `icon-button ${className}`); button.type = "button"; button.setAttribute("aria-label", label); button.title = label; button.append(icon(symbol)); button.addEventListener("click", handler); return button; }
  function showError(id, message) { $(id).textContent = message; $(id).hidden = false; }
  function hideError(id) { $(id).hidden = true; $(id).textContent = ""; }
  function toast(message) { clearTimeout(state.toastTimer); $("toast-message").textContent = message; $("toast").hidden = false; state.toastTimer = setTimeout(() => { $("toast").hidden = true; $("toast-message").textContent = ""; }, 6500); }
  function requestScope(name) {
    state.requests.get(name)?.abort();
    const controller = new AbortController(), session = state.session, sequence = (state.sequences.get(name) || 0) + 1;
    state.requests.set(name, controller); state.sequences.set(name, sequence);
    return { controller, current: () => state.sequences.get(name) === sequence && state.session === session };
  }
  function clearReveals() {
    state.secretEpoch++;
    for (const controller of state.revealRequests) controller.abort();
    state.revealRequests.clear();
    for (const reveal of state.reveals) { clearTimeout(reveal.timer); reveal.node.textContent = ""; reveal.node.hidden = true; if (reveal.button) reveal.button.disabled = false; }
    state.reveals.clear();
    $("edit-revealed").textContent = ""; $("edit-revealed").hidden = true;
  }
  function abortAll() { for (const controller of state.requests.values()) controller.abort(); state.requests.clear(); for (const [name, sequence] of state.sequences) state.sequences.set(name, sequence + 1); clearTimeout(state.projectSearchTimer); clearReveals(); }
  function sessionError(error) { if (error.status !== 401 || !state.session) return false; showLogin("Сессия завершена или токен отклонён. Войдите снова."); $("token-input").focus(); return true; }
  function showLogin(message = "") {
    closeSecurityHelp();
    abortAll(); state.session = null; state.projects = []; state.catalog.clear(); state.groups = []; state.groupTree = null; state.variables = []; state.active = null; state.edit = null; state.deletion = null; state.creation = null; state.busy = false; state.projectPage = 0; state.groupPage = 0; state.projectQuery = "";
    for (const id of ["edit-dialog", "delete-dialog", "create-dialog", "source-dialog"]) $(id).close();
    for (const id of ["token-input", "edit-value", "create-value", "variable-search"]) $(id).value = "";
    for (const id of ["variables-body", "target-projects", "create-results-list"]) $(id).replaceChildren();
    for (const id of ["edit-key", "edit-resource", "edit-scope", "delete-key", "delete-resource", "delete-scope", "edit-latest-value", "user-name", "toast-message"]) $(id).textContent = "";
    trackingNavigation.clear(); infrastructureEditor.clear(); showWorkspace("variables"); $("toast").hidden = true; $("app-screen").hidden = true; $("boot-screen").hidden = true; $("login-screen").hidden = false; $("bulk-button").disabled = true; resetResource();
    hideError("login-error"); if (message) showError("login-error", message);
  }
  async function enterSession(session) {
    abortAll(); state.session = session; state.active = null; state.projects = []; state.catalog.clear(); state.variables = []; state.groupTree = null; state.groups = [];
    $("login-screen").hidden = true; $("boot-screen").hidden = true; $("app-screen").hidden = false;
    $("user-name").textContent = session.user?.name || session.user?.username || "GitLab";
    $("server-label").textContent = session.demo ? "Демо-среда" : state.config.gitlab_url?.replace(/^https?:\/\//, "");
    $("demo-badge").hidden = !session.demo; $("demo-notice").hidden = !session.demo;
    resetResource(); await trackingNavigation.load();
  }
  function loginPending(pending) { state.loginPending = pending; $("login-submit").disabled = pending; $("demo-login").disabled = pending; $("token-input").disabled = pending; $("login-submit").textContent = pending ? "Подключаемся…" : "Подключиться"; }
  $("login-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (state.loginPending) return;
    let token = $("token-input").value.trim(); if (!token) return; $("token-input").value = ""; hideError("login-error"); loginPending(true);
    try { await enterSession(await api("/api/session", { method: "POST", body: JSON.stringify({ token }) })); }
    catch (error) { showError("login-error", error.message); }
    finally { token = ""; loginPending(false); }
  });
  $("demo-login").addEventListener("click", async () => { if (state.loginPending) return; hideError("login-error"); loginPending(true); try { await enterSession(await api("/api/demo-session", { method: "POST", body: "{}" })); } catch (error) { showError("login-error", error.message); } finally { loginPending(false); } });
  $("logout-button").addEventListener("click", async () => { $("logout-button").disabled = true; clearReveals(); try { await api("/api/session", { method: "DELETE" }); showLogin(); $("token-input").focus(); } catch (error) { if (!sessionError(error)) toast(error.message); } finally { $("logout-button").disabled = false; } });

  function resourceButton(resource, handler) {
    const button = el("button", "resource-button"); button.type = "button"; button.setAttribute("aria-current", String(resourceID(state.active) === resourceID(resource)));
    const body = el("span", "resource-button-text"); body.append(el("strong", "", resource.name), el("span", "", resourceName(resource)));
    button.append(el("span", `resource-symbol ${resource.kind === "groups" ? "group-symbol" : ""}`, resource.kind === "groups" ? "G" : "P"), body);
    button.addEventListener("click", handler || (() => selectResource(resource))); return button;
  }
  const trackingNavigation = window.createTrackingNavigation({ state, api, el, icon, requestScope, sessionError, resetResource, selectResource, resourceName, resourceID });
  const infrastructureEditor = window.createInfrastructureEditor({ state, api, el, icon, requestScope, sessionError, toast });
  function showWorkspace(view) {
    clearReveals(); closeSecurityHelp();
    for (const name of ["variables", "infrastructure"]) {
      const active = name === view;
      $(`${name}-tab`).setAttribute("aria-selected", String(active)); $(`${name}-tab`).tabIndex = active ? 0 : -1;
      $(`${name}-workspace`).hidden = !active;
    }
    $("tracking-navigation").hidden = view !== "variables"; $("infrastructure-navigation").hidden = view !== "infrastructure";
  }
  for (const name of ["variables", "infrastructure"]) {
    $(`${name}-tab`).addEventListener("click", () => showWorkspace(name));
    $(`${name}-tab`).addEventListener("keydown", (event) => {
      if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
      event.preventDefault(); const next = event.key === "Home" ? "variables" : event.key === "End" ? "infrastructure" : name === "variables" ? "infrastructure" : "variables";
      showWorkspace(next); $(`${next}-tab`).focus();
    });
  }
  function closeSecurityHelp(restoreFocus = false) {
    $("security-help").open = false;
    $("security-help-toggle").setAttribute("aria-expanded", "false");
    if (restoreFocus) $("security-help-toggle").focus();
  }
  $("security-help").addEventListener("toggle", () => $("security-help-toggle").setAttribute("aria-expanded", String($("security-help").open)));
  document.addEventListener("click", (event) => { if (!$("security-help").contains(event.target)) closeSecurityHelp(); });
  document.addEventListener("keydown", (event) => { if (event.key === "Escape" && $("security-help").open) { event.preventDefault(); closeSecurityHelp(true); } });
  function tableState(title, message) { $("table-scroll").hidden = true; $("variables-state").hidden = false; $("variables-state").replaceChildren(el("h2", "", title), el("p", "", message)); }
  function resetResource() {
    clearReveals(); state.active = null; state.variables = []; $("variables-body").replaceChildren(); $("breadcrumb-resource").textContent = "Выберите проект или группу"; $("resource-subtitle").textContent = "Выберите проект или группу слева."; $("variable-count").hidden = true;
    for (const id of ["refresh-button", "create-button", "variable-search", "environment-select"]) $(id).disabled = true;
    $("table-summary").textContent = "Источник не выбран"; hideError("variables-error"); tableState("Добавьте проект или группу", "Выберите, что отслеживать, затем откройте источник в меню слева.");
  }
  function selectResource(resource) {
    if (resourceID(resource) === resourceID(state.active)) return;
    clearReveals(); state.active = resource; state.variables = []; $("variable-search").value = ""; $("environment-select").replaceChildren(new Option("Все окружения", ""));
    $("resource-kind-label").textContent = resource.kind === "groups" ? "Группы" : "Проекты"; $("breadcrumb-resource").textContent = resource.name;
    $("resource-subtitle").textContent = `${resource.kind === "groups" ? "Группа" : "Проект"}: ${resourceName(resource)}`; $("variable-count").hidden = true;
    trackingNavigation.render(); loadVariables();
  }
  async function loadVariables() {
    if (!state.active || !state.session) return;
    clearReveals(); const scope = requestScope("variables"), resource = state.active;
    for (const id of ["refresh-button", "variable-search", "environment-select", "create-button"]) $(id).disabled = true;
    hideError("variables-error"); $("table-summary").textContent = "Загрузка…"; tableState("Получаем переменные", "Значения секретов в список не включаются.");
    try {
      const data = await api(resourcePath(resource), { signal: scope.controller.signal }); if (!scope.current() || resourceID(resource) !== resourceID(state.active)) return;
      state.variables = data.variables || []; updateEnvironments(); renderVariables();
    } catch (error) {
      if (error.name === "AbortError" || !scope.current() || sessionError(error)) return;
      state.variables = []; $("variables-body").replaceChildren(); showError("variables-error", error.message);
      if (error.status === 403 && resource.kind === "groups") tableState("Нет доступа к переменным группы", "Для групповых переменных нужна роль Owner или отдельное разрешение GitLab. Если нужны переменные проекта, добавьте его в разделе «Проекты».");
      else tableState("Не удалось загрузить переменные", "Проверьте сообщение об ошибке и повторите загрузку.");
      $("table-summary").textContent = "Ошибка загрузки";
    }
    finally { if (scope.current() && state.session) for (const id of ["refresh-button", "variable-search", "environment-select", "create-button"]) $(id).disabled = false; }
  }
  function updateEnvironments() { const previous = $("environment-select").value, scopes = [...new Set(state.variables.map((variable) => variable.environment_scope))].sort(); $("environment-select").replaceChildren(new Option("Все окружения", ""), ...scopes.map((scope) => new Option(scope === "*" ? "* (любое)" : scope, scope))); if (scopes.includes(previous)) $("environment-select").value = previous; }
  function renderVariables() {
    clearReveals(); const query = $("variable-search").value.trim().toLocaleLowerCase(), environment = $("environment-select").value;
    const variables = state.variables.filter((variable) => (!environment || variable.environment_scope === environment) && (!query || `${variable.key} ${variable.description || ""}`.toLocaleLowerCase().includes(query))).sort((a, b) => a.key.localeCompare(b.key) || a.environment_scope.localeCompare(b.environment_scope));
    const fragment = document.createDocumentFragment();
    for (const variable of variables) {
      const row = el("tr"), valueCell = el("td"), title = el("div", "variable-title"), value = el("pre", "variable-value", secret(variable) ? "••••••••" : variable.value === "" ? "Пустое значение" : variable.value ?? "");
      title.append(el("strong", "variable-key", variable.key));
      for (const badge of [variable.hidden ? "Hidden" : variable.masked ? "Masked" : "", variable.protected ? "Protected" : ""]) if (badge) title.append(el("span", "badge", badge));
      if (secret(variable)) value.classList.add("secret-placeholder");
      valueCell.append(title, value); if (variable.description) valueCell.append(el("p", "variable-description", variable.description));
      const meta = el("td", "variable-meta"); meta.append(el("span", "scope-badge", variable.environment_scope), el("span", "variable-type", variable.variable_type === "file" ? "File" : "Variable"));
      const actions = el("td", "actions-cell"), controls = el("div", "row-actions");
      if (secret(variable) && !variable.hidden) { const revealed = el("pre", "revealed-value"); revealed.hidden = true; valueCell.append(revealed); const revealButton = action(`Показать ${variable.key}, окружение ${variable.environment_scope}`, "eye", () => revealVariable(variable, state.active, revealed, revealButton)); controls.append(revealButton); }
      controls.append(action(`Изменить ${variable.key}, окружение ${variable.environment_scope}`, "pencil", () => openEditor(variable)), action(`Удалить ${variable.key}, окружение ${variable.environment_scope}`, "trash", () => openDelete(variable), "danger-icon")); actions.append(controls); row.append(valueCell, meta, actions); fragment.append(row);
    }
    $("variables-body").replaceChildren(fragment); $("variable-count").hidden = false; $("variable-count").textContent = state.variables.length; $("table-summary").textContent = `Показано ${variables.length} из ${state.variables.length}`;
    if (variables.length) { $("variables-state").hidden = true; $("table-scroll").hidden = false; }
    else tableState(state.variables.length ? "Ничего не найдено" : "Переменных пока нет", state.variables.length ? "Измените поиск или фильтр окружения." : "Нажмите «Создать», чтобы добавить переменную.");
  }
  $("refresh-button").addEventListener("click", loadVariables); $("variable-search").addEventListener("input", renderVariables); $("environment-select").addEventListener("change", renderVariables);
  async function revealVariable(variable, resource, node, button) {
    if (variable.hidden || !state.session) return;
    const controller = new AbortController(), epoch = state.secretEpoch, session = state.session; state.revealRequests.add(controller); button.disabled = true;
    try {
      const data = await api(`${variablePath(resource, variable)}/reveal`, { method: "POST", body: JSON.stringify({ environment_scope: variable.environment_scope, version: variable.version }), signal: controller.signal });
      if (session !== state.session || epoch !== state.secretEpoch || !node.isConnected) return;
      node.textContent = data.value; node.hidden = false;
      const reveal = { node, button, timer: null }; reveal.timer = setTimeout(() => { node.textContent = ""; node.hidden = true; button.disabled = false; state.reveals.delete(reveal); }, Math.min(Number(data.expires_in) || 30, 30) * 1000); state.reveals.add(reveal);
    } catch (error) { if (error.name !== "AbortError" && session === state.session && !sessionError(error)) { if ($("edit-dialog").open) showError("edit-error", error.message); else toast(error.message); } }
    finally { state.revealRequests.delete(controller); if (node.hidden) button.disabled = false; }
  }

  function setEditBusy(busy) { state.busy = busy; for (const id of ["edit-value", "edit-close", "edit-cancel", "edit-reload", "edit-reveal"]) $(id).disabled = busy; $("edit-save").textContent = busy ? "Сохраняем…" : "Сохранить"; updateDirty(); }
  function openEditor(variable) {
    clearReveals(); state.edit = { variable: { ...variable }, resource: { ...state.active }, touched: false }; state.editReloading = false;
    $("edit-resource").textContent = resourceName(state.active); $("edit-key").textContent = variable.key; $("edit-scope").textContent = variable.environment_scope;
    $("edit-intro").textContent = state.session.demo ? "Демо: значение изменится только в памяти приложения." : "Изменится только значение; окружение и параметры переменной сохранятся.";
    $("edit-value").value = secret(variable) ? "" : variable.value ?? ""; $("edit-value").placeholder = secret(variable) ? "Введите новое секретное значение" : ""; $("edit-value").classList.toggle("secret-input", secret(variable));
    $("edit-secret-note").hidden = !secret(variable); $("edit-reveal").hidden = !secret(variable) || variable.hidden; $("edit-latest").hidden = true; $("edit-latest-value").textContent = ""; $("edit-conflict").hidden = true; hideError("edit-error"); setEditBusy(false); $("edit-dialog").showModal(); $("edit-value").focus();
  }
  function closeEditor() { if (state.busy || state.editReloading) return; clearReveals(); $("edit-dialog").close(); $("edit-value").value = ""; $("edit-latest-value").textContent = ""; state.edit = null; $("refresh-button").focus(); }
  function updateDirty() { if (!state.edit) return; const dirty = secret(state.edit.variable) ? state.edit.touched : $("edit-value").value !== (state.edit.variable.value ?? ""); $("edit-dirty").textContent = dirty ? "Есть несохранённые изменения" : "Изменений нет"; $("edit-save").disabled = !dirty || state.busy || state.editReloading; }
  $("edit-value").addEventListener("input", () => { if (state.edit) state.edit.touched = true; updateDirty(); });
  $("edit-reveal").addEventListener("click", () => { if (state.edit) revealVariable(state.edit.variable, state.edit.resource, $("edit-revealed"), $("edit-reveal")); });
  $("edit-close").addEventListener("click", closeEditor); $("edit-cancel").addEventListener("click", closeEditor); $("edit-dialog").addEventListener("cancel", (event) => { event.preventDefault(); closeEditor(); });
  $("edit-value").addEventListener("keydown", (event) => { if ((event.ctrlKey || event.metaKey) && event.key === "Enter" && !$("edit-save").disabled) { event.preventDefault(); $("edit-form").requestSubmit(); } });
  $("edit-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (!state.edit || state.busy || state.editReloading || $("edit-save").disabled) return;
    const edit = state.edit, session = state.session; clearReveals(); setEditBusy(true); hideError("edit-error"); $("edit-conflict").hidden = true;
    try {
      const result = await api(variablePath(edit.resource, edit.variable), { method: "PUT", body: JSON.stringify({ value: $("edit-value").value, environment_scope: edit.variable.environment_scope, version: edit.variable.version }) });
      if (session !== state.session || state.edit !== edit) return;
      const index = state.variables.findIndex((variable) => variableID(variable) === variableID(edit.variable)); if (index >= 0) state.variables[index] = result.variable;
      setEditBusy(false); closeEditor(); renderVariables(); toast(`Переменная ${edit.variable.key} сохранена`);
    } catch (error) { if (!sessionError(error) && session === state.session) { showError("edit-error", error.message); $("edit-conflict").hidden = error.status !== 409; } }
    finally { if (session === state.session && state.edit === edit) setEditBusy(false); }
  });
  $("edit-reload").addEventListener("click", async () => {
    if (!state.edit || state.busy || state.editReloading) return;
    const edit = state.edit, session = state.session; state.editReloading = true; $("edit-reload").disabled = true; updateDirty(); clearReveals();
    try {
      const data = await api(resourcePath(edit.resource)); if (session !== state.session || state.edit !== edit) return;
      const fresh = data.variables.find((variable) => variableID(variable) === variableID(edit.variable)); if (!fresh) throw new APIError("Переменная удалена или недоступна. Закройте редактор и обновите список.", 404, "not_found");
      edit.variable = { ...fresh }; state.variables = data.variables; $("edit-conflict").hidden = true; $("edit-latest").hidden = secret(fresh); $("edit-latest-value").textContent = secret(fresh) ? "" : fresh.value ?? ""; $("edit-reveal").hidden = !secret(fresh) || fresh.hidden; $("edit-secret-note").hidden = !secret(fresh); $("edit-value").classList.toggle("secret-input", secret(fresh)); renderVariables();
    } catch (error) { if (!sessionError(error) && session === state.session) showError("edit-error", error.message); }
    finally { if (session === state.session && state.edit === edit) { state.editReloading = false; $("edit-reload").disabled = false; updateDirty(); } }
  });

  function openDelete(variable) { clearReveals(); state.deletion = { variable: { ...variable }, resource: { ...state.active } }; $("delete-resource").textContent = resourceName(state.active); $("delete-key").textContent = variable.key; $("delete-scope").textContent = variable.environment_scope; hideError("delete-error"); $("delete-submit").disabled = false; $("delete-cancel").disabled = false; $("delete-dialog").showModal(); $("delete-cancel").focus(); }
  function closeDelete() { if (state.busy) return; $("delete-dialog").close(); state.deletion = null; $("refresh-button").focus(); }
  $("delete-cancel").addEventListener("click", closeDelete); $("delete-dialog").addEventListener("cancel", (event) => { event.preventDefault(); closeDelete(); });
  $("delete-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (!state.deletion || state.busy) return;
    const deletion = state.deletion, session = state.session; state.busy = true; $("delete-submit").disabled = true; $("delete-cancel").disabled = true; hideError("delete-error");
    try { await api(variablePath(deletion.resource, deletion.variable), { method: "DELETE", body: JSON.stringify({ environment_scope: deletion.variable.environment_scope, version: deletion.variable.version }) }); if (session !== state.session) return; state.variables = state.variables.filter((variable) => variableID(variable) !== variableID(deletion.variable)); state.busy = false; closeDelete(); updateEnvironments(); renderVariables(); toast(`Переменная ${deletion.variable.key} удалена`); }
    catch (error) { if (!sessionError(error) && session === state.session) showError("delete-error", error.message); }
    finally { if (session === state.session) { state.busy = false; $("delete-submit").disabled = false; $("delete-cancel").disabled = false; } }
  });
  function createMode() { const bulk = $("create-mode").value === "bulk"; $("create-targets").hidden = !bulk; $("create-single-target").hidden = bulk; $("create-title").textContent = bulk ? "Создать в нескольких проектах" : "Создать переменную"; $("create-submit").textContent = bulk ? "Создать в выбранных проектах" : "Создать переменную"; $("create-confirm-text").textContent = bulk ? "Подтверждаю создание переменной во всех отмеченных проектах." : "Подтверждаю создание переменной в выбранном источнике."; $("create-confirm").checked = false; renderTargetProjects(); }
  function openCreate(mode = "single") {
    clearReveals(); state.creation = { resource: state.active ? { ...state.active } : null, selected: new Set(), completed: false }; state.busy = false;
    $("create-form").reset(); $("create-mode").value = mode; $("create-mode").querySelector('option[value="single"]').disabled = !state.active; $("create-resource").textContent = resourceName(state.active); $("create-scope").value = "*"; $("create-raw").checked = true; $("target-search").value = ""; $("create-value").value = ""; $("create-value").classList.remove("secret-input"); $("create-secret-help").hidden = true; $("create-results").hidden = true; $("create-results-list").replaceChildren(); hideError("create-error"); setCreateBusy(false); createMode(); $("create-dialog").showModal(); $(mode === "bulk" ? "target-search" : "create-key").focus();
  }
  function closeCreate() { if (state.busy) return; $("create-dialog").close(); $("create-value").value = ""; $("create-results-list").replaceChildren(); state.creation = null; $("create-button").focus(); }
  function projectChoices() { const query = $("target-search").value.trim().toLocaleLowerCase(); return [...state.catalog.values()].filter((project) => !project.archived && (!query || resourceName(project).toLocaleLowerCase().includes(query))).sort((a, b) => resourceName(a).localeCompare(resourceName(b))); }
  function renderTargetProjects() {
    if (!state.creation) return;
    const fragment = document.createDocumentFragment();
    for (const project of projectChoices()) {
      const label = el("label", "project-check"), checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.checked = state.creation.selected.has(project.id); checkbox.disabled = state.busy || state.creation.completed;
      checkbox.addEventListener("change", () => { if (!state.creation) return; if (checkbox.checked && state.creation.selected.size >= 50) { checkbox.checked = false; showError("create-error", "Можно выбрать не более 50 проектов."); return; } if (checkbox.checked) state.creation.selected.add(project.id); else state.creation.selected.delete(project.id); $("selected-count").textContent = state.creation.selected.size; $("create-confirm").checked = false; });
      label.append(checkbox, el("span", "", resourceName(project))); fragment.append(label);
    }
    if (!fragment.childNodes.length) fragment.append(el("p", "field-help", "Нет отслеживаемых проектов для выбора. Добавьте проект или группу в меню слева."));
    $("target-projects").replaceChildren(fragment); $("selected-count").textContent = state.creation.selected.size;
  }
  function setCreateBusy(busy) { state.busy = busy; for (const control of $("create-form").querySelectorAll("input,select,textarea,button")) control.disabled = busy || !!state.creation?.completed; if (!busy && state.creation?.completed) { $("create-close").disabled = false; $("create-cancel").disabled = false; $("create-cancel").textContent = "Закрыть"; } else $("create-cancel").textContent = "Отмена"; }
  $("create-button").addEventListener("click", () => openCreate("single")); $("bulk-button").addEventListener("click", () => openCreate("bulk")); $("create-close").addEventListener("click", closeCreate); $("create-cancel").addEventListener("click", closeCreate); $("create-dialog").addEventListener("cancel", (event) => { event.preventDefault(); closeCreate(); }); $("create-mode").addEventListener("change", createMode); $("target-search").addEventListener("input", renderTargetProjects);
  $("select-all-projects").addEventListener("click", () => { if (!state.creation || state.busy) return; const choices = projectChoices(), allSelected = choices.length > 0 && choices.every((project) => state.creation.selected.has(project.id)); if (allSelected) for (const project of choices) state.creation.selected.delete(project.id); else { for (const project of choices) { if (state.creation.selected.size >= 50) break; state.creation.selected.add(project.id); } } $("create-confirm").checked = false; renderTargetProjects(); });
  $("create-visibility").addEventListener("change", () => { const isSecret = $("create-visibility").value !== "public"; $("create-value").classList.toggle("secret-input", isSecret); $("create-secret-help").hidden = !isSecret; });
  $("create-form").addEventListener("submit", async (event) => {
    event.preventDefault(); if (!state.creation || state.busy || state.creation.completed) return;
    const creation = state.creation, session = state.session, bulk = $("create-mode").value === "bulk";
    if (bulk && (creation.selected.size === 0 || creation.selected.size > 50)) { showError("create-error", "Выберите от 1 до 50 проектов."); return; }
    if (!bulk && !creation.resource) { showError("create-error", "Сначала выберите проект или группу."); return; }
    const visibility = $("create-visibility").value;
    const variable = { key: $("create-key").value.trim(), value: $("create-value").value, environment_scope: $("create-scope").value.trim(), variable_type: $("create-type").value, masked: visibility !== "public", hidden: visibility === "hidden", protected: $("create-protected").checked, raw: $("create-raw").checked, description: $("create-description").value.trim() || null };
    if (!/^[A-Za-z0-9_]{1,255}$/.test(variable.key) || !variable.environment_scope || [...variable.value].length > 10000) { showError("create-error", "Проверьте ключ, окружение и значение (до 10 000 символов)."); return; }
    hideError("create-error"); setCreateBusy(true);
    try {
      if (bulk) {
        const data = await api("/api/variables/bulk", { method: "POST", body: JSON.stringify({ project_ids: [...creation.selected], variable }) }); if (session !== state.session || state.creation !== creation) return;
        $("create-value").value = ""; variable.value = ""; creation.completed = true; $("create-results").hidden = false; $("create-results-title").textContent = `Создано: ${data.created}; ошибок: ${data.failed}`;
        const fragment = document.createDocumentFragment(); for (const result of data.results || []) { const name = resourceName(state.catalog.get(result.project_id)) || `Проект ${result.project_id}`; fragment.append(el("li", result.status === "created" ? "result-success" : "result-failure", `${name}: ${result.status === "created" ? "создано" : result.message || "ошибка"}`)); } $("create-results-list").replaceChildren(fragment);
        if (state.active?.kind === "projects" && creation.selected.has(state.active.id)) loadVariables();
      } else {
        await api(resourcePath(creation.resource), { method: "POST", body: JSON.stringify(variable) }); if (session !== state.session || state.creation !== creation) return;
        $("create-value").value = ""; variable.value = ""; state.busy = false; closeCreate(); toast(`Переменная ${variable.key} создана`); loadVariables();
      }
    } catch (error) { if (!sessionError(error) && session === state.session) showError("create-error", error.message); }
    finally { variable.value = ""; if (session === state.session && state.creation === creation) setCreateBusy(false); }
  });

  $("toast-close").addEventListener("click", () => { clearTimeout(state.toastTimer); $("toast").hidden = true; $("toast-message").textContent = ""; });
  window.addEventListener("blur", clearReveals);
  document.addEventListener("visibilitychange", () => { if (document.hidden) clearReveals(); });
  window.addEventListener("pagehide", () => { clearReveals(); $("edit-value").value = ""; $("create-value").value = ""; $("token-input").value = ""; });
  async function initialize() { try { state.config = await api("/api/config"); $("gitlab-instance").value = state.config.gitlab_url; $("demo-login").hidden = !state.config.demo; try { await enterSession(await api("/api/session")); } catch (error) { showLogin(error.status === 401 ? "" : error.message); } } catch (error) { showLogin(error.message); } }
  initialize();
})();
