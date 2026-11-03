"use strict";

// Tracking contains resource IDs only. Variable values and tokens never enter it.
window.createTrackingNavigation = function ({ state, api, el, icon, requestScope, sessionError, resetResource, selectResource, resourceName, resourceID }) {
  const $ = (id) => document.getElementById(id);
  const nav = { sources: [], version: 0, loaded: new Map(), expanded: new Set(), ready: false, saving: false, discovery: [], nextPage: 0, query: "", kind: "groups", searchTimer: null };
  const key = (source) => `${source.kind}:${source.id}`;
  const same = (a, b) => a.kind === b.kind && a.id === b.id;
  const copySource = (source) => ({ kind: source.kind, id: source.id, excluded: (source.excluded || []).map((item) => ({ kind: item.kind, id: item.id })) });
  const message = (text = "") => { $("tracking-message").textContent = text; $("tracking-message").hidden = !text; };
  const searchMessage = (text = "") => { $("source-message").textContent = text; $("source-message").hidden = !text; };

  function groupPath(group) { return group.full_path || ""; }
  function namespace(project) { return (project.path_with_namespace || "").split("/").slice(0, -1).join("/"); }
  function parentPath(path) { return path.split("/").slice(0, -1).join("/"); }
  function within(path, root) { return path === root || path.startsWith(`${root}/`); }
  function descriptor(source) { return { ...source, name: `${source.kind === "groups" ? "Группа" : "Проект"} #${source.id}` }; }

  function groupData(source, loaded) {
    const root = { ...loaded.data.group, kind: "groups" };
    const groups = new Map([[root.id, root]]);
    for (const group of loaded.data.groups || []) groups.set(group.id, { ...group, kind: "groups" });
    const byPath = new Map([...groups.values()].map((group) => [groupPath(group), group]));
    const excluded = new Set((source.excluded || []).map(key));
    const excludedPaths = [...groups.values()].filter((group) => excluded.has(key(group))).map(groupPath);
    const blocked = (resource) => {
      if (excluded.has(key(resource))) return true;
      const path = resource.kind === "groups" ? groupPath(resource) : namespace(resource);
      return excludedPaths.some((excludedPath) => excludedPath && within(path, excludedPath));
    };
    const visibleGroups = [...groups.values()].filter((group) => !blocked(group));
    const projects = (loaded.data.projects || []).map((project) => ({ ...project, kind: "projects" })).filter((project) => !blocked(project));
    const children = new Map(visibleGroups.map((group) => [group.id, []]));
    const visibleIDs = new Set(visibleGroups.map((group) => group.id));
    const nearestGroup = (path) => {
      let current = path;
      while (current) { const group = byPath.get(current); if (group && visibleIDs.has(group.id)) return group; current = parentPath(current); }
      return root;
    };
    for (const group of visibleGroups) {
      if (group.id === root.id) continue;
      const directParent = groups.get(group.parent_id);
      const parent = directParent && visibleIDs.has(directParent.id) ? directParent : nearestGroup(parentPath(groupPath(group)));
      if (parent.id !== group.id && children.has(parent.id)) children.get(parent.id).push(group);
    }
    for (const project of projects) {
      const parent = nearestGroup(namespace(project));
      if (children.has(parent.id)) children.get(parent.id).push(project);
    }
    for (const entries of children.values()) entries.sort((a, b) => (a.kind === b.kind ? resourceName(a).localeCompare(resourceName(b), "ru") : a.kind === "groups" ? -1 : 1));
    return { root, groups: visibleGroups, projects, children };
  }

  function syncCatalog() {
    const visible = new Set();
    state.catalog.clear();
    for (const source of nav.sources) {
      const loaded = nav.loaded.get(key(source));
      if (loaded?.status !== "ready") continue;
      if (source.kind === "projects") { const project = { ...loaded.data.project, kind: "projects" }; state.catalog.set(project.id, project); visible.add(key(project)); }
      else {
        const tree = groupData(source, loaded);
        for (const group of tree.groups) visible.add(key(group));
        for (const project of tree.projects) { state.catalog.set(project.id, project); visible.add(key(project)); }
      }
    }
    $("bulk-button").disabled = state.catalog.size === 0;
    if (state.active && !visible.has(resourceID(state.active))) resetResource();
  }

  function removeButton(source, resource, root) {
    const button = el("button", "tracking-remove icon-button"); button.type = "button"; button.disabled = nav.saving;
    const label = root ? `Перестать отслеживать ${resourceName(resource) || resource.name}` : `Не отслеживать ${resourceName(resource) || resource.name} в этой группе`;
    button.title = label; button.setAttribute("aria-label", label); button.append(icon("close"));
    button.addEventListener("click", () => {
      const next = root ? nav.sources.filter((item) => !same(item, source)).map(copySource) : nav.sources.map((item) => same(item, source) ? { ...copySource(item), excluded: [...(item.excluded || []), { kind: resource.kind, id: resource.id }].filter((entry, index, list) => list.findIndex((other) => same(other, entry)) === index) } : copySource(item));
      save(next);
    });
    return button;
  }

  function treeNode(source, resource, tree, root = false, ancestry = new Set()) {
    const li = el("li", `tracking-node${root ? " tracking-root" : ""}`), row = el("div", "tracking-row");
    const children = resource.kind === "groups" ? tree.children.get(resource.id) || [] : [];
    const expandID = `${key(source)}/${key(resource)}`, open = nav.expanded.has(expandID);
    if (children.length) {
      const toggle = el("button", "tracking-toggle", open ? "⌄" : "›"); toggle.type = "button";
      toggle.setAttribute("aria-expanded", String(open)); toggle.setAttribute("aria-label", `${open ? "Свернуть" : "Развернуть"} ${resource.name}`);
      toggle.addEventListener("click", () => { if (open) nav.expanded.delete(expandID); else nav.expanded.add(expandID); render(); }); row.append(toggle);
    } else row.append(el("span", "tracking-toggle-space"));
    const choose = el("button", "tracking-choose"); choose.type = "button"; choose.title = resourceName(resource); choose.setAttribute("aria-current", String(resourceID(state.active) === key(resource)));
    const symbol = el("span", `tracking-symbol ${resource.kind === "groups" ? "is-group" : ""}`, resource.kind === "groups" ? "G" : "P"); symbol.setAttribute("aria-hidden", "true");
    choose.append(symbol, el("span", "tracking-name", resource.name)); choose.setAttribute("aria-label", `${resource.kind === "groups" ? "Группа" : "Проект"} ${resourceName(resource) || resource.name}`);
    choose.addEventListener("click", () => selectResource(resource)); row.append(choose, removeButton(source, resource, root)); li.append(row);
    if (open && children.length && !ancestry.has(resource.id)) {
      const nested = el("ul", "tracking-children"), nextAncestry = new Set(ancestry); nextAncestry.add(resource.id);
      for (const child of children) nested.append(treeNode(source, child, tree, false, nextAncestry)); li.append(nested);
    }
    return li;
  }

  function pendingNode(source, loaded) {
    const li = el("li", "tracking-pending"), row = el("div", "tracking-row"), resource = loaded?.resource || descriptor(source);
    row.append(el("span", "tracking-pending-name", resourceName(resource) || resource.name), removeButton(source, resource, true)); li.append(row);
    if (loaded?.status === "error") {
      li.append(el("p", "tracking-source-error", loaded.message));
      const retry = el("button", "text-button", "Повторить загрузку"); retry.type = "button"; retry.disabled = nav.saving; retry.addEventListener("click", () => hydrate(source)); li.append(retry);
    } else li.append(el("p", "tracking-loading", "Загрузка источника…"));
    return li;
  }

  function render() {
    const fragment = document.createDocumentFragment();
    for (const source of nav.sources) {
      const loaded = nav.loaded.get(key(source));
      if (loaded?.status !== "ready") fragment.append(pendingNode(source, loaded));
      else if (source.kind === "projects") fragment.append(treeNode(source, { ...loaded.data.project, kind: "projects" }, { children: new Map() }, true));
      else {
        const tree = groupData(source, loaded), root = treeNode(source, tree.root, tree, true);
        if (source.excluded?.length) {
          const restore = el("button", "text-button tracking-restore", `Восстановить исключённые (${source.excluded.length})`); restore.type = "button"; restore.disabled = nav.saving;
          restore.addEventListener("click", () => save(nav.sources.map((item) => same(item, source) ? { kind:item.kind, id:item.id, excluded:[] } : copySource(item)))); root.append(restore);
        }
        fragment.append(root);
      }
    }
    $("tracking-tree").replaceChildren(fragment); $("tracking-count").textContent = nav.sources.length;
    $("tracking-empty").hidden = nav.sources.length > 0 || !nav.ready;
    $("add-source").disabled = !nav.ready || nav.saving; $("tracking-refresh").disabled = nav.saving;
  }

  async function hydrate(source) {
    if (!state.session || !nav.sources.some((item) => same(item, source))) return;
    const scope = requestScope(`tracked-${key(source)}`), previous = nav.loaded.get(key(source));
    nav.loaded.set(key(source), { status: "loading", resource: previous?.resource }); syncCatalog(); render();
    try {
      const data = await api(source.kind === "groups" ? `/api/groups/${source.id}/tree` : `/api/projects/${source.id}`, { signal: scope.controller.signal });
      if (!scope.current() || !nav.sources.some((item) => same(item, source))) return;
      nav.loaded.set(key(source), { status: "ready", data, resource: { ...(data.group || data.project), kind: source.kind } });
    } catch (error) {
      if (error.name === "AbortError" || !scope.current() || sessionError(error)) return;
      nav.loaded.set(key(source), { status: "error", resource: previous?.resource, message: error.message });
    } finally { if (scope.current()) { syncCatalog(); render(); } }
  }

  async function hydrateAll() {
    const pending = [...nav.sources];
    await Promise.all(Array.from({ length: Math.min(4, pending.length) }, async () => { while (pending.length) await hydrate(pending.shift()); }));
  }
  async function load() {
    const scope = requestScope("tracking"); nav.ready = false; render(); message("Загрузка выбранных источников…");
    try {
      const data = await api("/api/tracking", { signal: scope.controller.signal }); if (!scope.current()) return;
      nav.sources = (data.sources || []).map(copySource); nav.version = data.version || 0; nav.loaded.clear(); nav.ready = true;
      for (const source of nav.sources) if (source.kind === "groups") nav.expanded.add(`${key(source)}/${key(source)}`);
      message(); syncCatalog(); render(); await hydrateAll();
    } catch (error) { if (error.name !== "AbortError" && scope.current() && !sessionError(error)) message(error.message); }
    finally { if (scope.current()) render(); }
  }

  async function save(next, candidate) {
    if (!nav.ready || nav.saving || !state.session) return false;
    const session = state.session; nav.saving = true; render(); renderResults(); searchMessage(candidate ? "Добавляем источник в меню…" : ""); message("Сохраняем выбранные источники…");
    try {
      const data = await api("/api/tracking", { method: "PUT", body: JSON.stringify({ sources: next, version: nav.version }) }); if (session !== state.session) return false;
      const oldKeys = new Set(nav.sources.map(key)); nav.sources = (data.sources || []).map(copySource); nav.version = data.version || 0;
      const retained = new Set(nav.sources.map(key)); for (const entry of nav.loaded.keys()) if (!retained.has(entry)) nav.loaded.delete(entry);
      if (candidate) nav.loaded.set(key(candidate), { status: "loading", resource: candidate });
      for (const source of nav.sources) if (!oldKeys.has(key(source))) { if (source.kind === "groups") nav.expanded.add(`${key(source)}/${key(source)}`); await hydrate(source); }
      message(); searchMessage(candidate ? "Источник добавлен в меню слева." : ""); syncCatalog(); render(); return true;
    } catch (error) {
      if (sessionError(error) || session !== state.session) return false;
      if (error.status === 409) { await load(); message("Список был изменён в другой вкладке. Загружена актуальная версия; повторите свой выбор."); }
      else message(error.message);
      searchMessage(error.message); return false;
    } finally { if (session === state.session) { nav.saving = false; render(); renderResults(); } }
  }

  function renderResults() {
    const fragment = document.createDocumentFragment();
    for (const resource of nav.discovery) {
      const row = el("div", "source-result"), label = el("div", "source-result-label"); label.append(el("strong", "", resource.name), el("span", "", resourceName(resource)));
      const followed = nav.sources.some((source) => same(source, resource));
      const button = el("button", "button button-secondary", followed ? "Отслеживается" : "Отслеживать"); button.type = "button"; button.disabled = followed || nav.saving;
      button.setAttribute("aria-label", `${followed ? "Отслеживается" : "Отслеживать"} ${resourceName(resource) || resource.name}`);
      button.addEventListener("click", () => save([...nav.sources.map(copySource), { kind: resource.kind, id: resource.id, excluded: [] }], resource)); row.append(label, button); fragment.append(row);
    }
    $("source-results").replaceChildren(fragment); $("source-more").hidden = !nav.nextPage; $("source-more").disabled = nav.saving;
  }
  async function search(append = false) {
    clearTimeout(nav.searchTimer);
    if (!state.session || nav.saving || !$("source-dialog").open) return;
    const query = $("source-query").value.trim(), kind = $("source-kind").value;
    const scope = requestScope("source-search"), page = append ? nav.nextPage : 1; if (!page) return;
    if (!append) { nav.discovery = []; nav.nextPage = 0; renderResults(); }
    nav.query = query; nav.kind = kind; $("source-submit").disabled = true; $("source-more").disabled = true; searchMessage("Поиск…");
    try {
      const data = await api(`/api/${kind}?${new URLSearchParams({ search: query, page: String(page) })}`, { signal: scope.controller.signal }); if (!scope.current()) return;
      nav.discovery = [...new Map([...(append ? nav.discovery : []), ...(data[kind] || []).map((resource) => ({ ...resource, kind }))].map((resource) => [key(resource), resource])).values()];
      nav.nextPage = data.next_page || 0; renderResults(); searchMessage(nav.discovery.length ? "" : "Ничего не найдено. Проверьте название и права токена.");
    } catch (error) { if (error.name !== "AbortError" && scope.current() && !sessionError(error)) searchMessage(error.message); }
    finally { if (scope.current()) { $("source-submit").disabled = false; $("source-more").disabled = nav.saving; } }
  }
  function closeDiscovery() {
    if (nav.saving) return;
    clearTimeout(nav.searchTimer);
    state.requests.get("source-search")?.abort(); state.sequences.set("source-search", (state.sequences.get("source-search") || 0) + 1);
    $("source-dialog").close(); nav.discovery = []; nav.nextPage = 0; $("source-results").replaceChildren(); $("source-query").value = ""; searchMessage(); $("add-source").focus();
  }
  $("add-source").addEventListener("click", () => { if (!nav.ready || nav.saving) return; nav.discovery = []; nav.nextPage = 0; $("source-results").replaceChildren(); $("source-query").value = ""; $("source-kind").value = "groups"; $("source-more").hidden = true; $("source-submit").disabled = false; searchMessage(); $("source-dialog").showModal(); $("source-query").focus(); search(false); });
  $("tracking-refresh").addEventListener("click", load);
  $("source-search-form").addEventListener("submit", (event) => { event.preventDefault(); search(false); });
  $("source-more").addEventListener("click", () => { if ($("source-query").value.trim() !== nav.query || $("source-kind").value !== nav.kind) search(false); else search(true); });
  $("source-kind").addEventListener("change", () => { clearTimeout(nav.searchTimer); state.requests.get("source-search")?.abort(); state.sequences.set("source-search", (state.sequences.get("source-search") || 0) + 1); nav.discovery = []; nav.nextPage = 0; renderResults(); search(false); });
  $("source-query").addEventListener("input", () => {
    clearTimeout(nav.searchTimer);
    state.requests.get("source-search")?.abort(); state.sequences.set("source-search", (state.sequences.get("source-search") || 0) + 1);
    nav.discovery = []; nav.nextPage = 0; renderResults(); $("source-submit").disabled = false;
    nav.searchTimer = setTimeout(() => search(false), 250);
  });
  $("source-close").addEventListener("click", closeDiscovery); $("source-done").addEventListener("click", closeDiscovery); $("source-dialog").addEventListener("cancel", (event) => { event.preventDefault(); closeDiscovery(); });

  function clear() { clearTimeout(nav.searchTimer); nav.sources = []; nav.version = 0; nav.loaded.clear(); nav.expanded.clear(); nav.ready = false; nav.saving = false; nav.discovery = []; nav.nextPage = 0; state.catalog.clear(); $("source-dialog").close(); $("source-query").value = ""; $("source-results").replaceChildren(); searchMessage(); message(); render(); }
  return { load, render, clear };
};
