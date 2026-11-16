import { runtimeMode } from "@/runtime";
import * as api from "./api/client";
import type { DBView, ExportBundle, FieldView } from "./types";

const $ = (s: string): any => document.querySelector(s);
const FIELD_TYPES = ["bool", "bit", "byte", "word", "dword", "int", "dint", "real", "string"];
const THEME_KEY = "plc-simulater-theme";

type Theme = "dark" | "light";

let timer: number | null = null;
let selected: number | null = null;
let dbsList: DBView[] = [];
let currentFields: FieldView[] = [];
let editingName: string | null = null;
let modalCtx: number | null = null;
let ctxNum: number | null = null;

function toast(msg: string, ok = true): void {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast show" + (ok ? "" : " err");
  clearTimeout(t._t);
  t._t = setTimeout(() => (t.className = "toast"), 2200);
}

function fmt(v: any): string {
  if (typeof v === "number") return Number.isInteger(v) ? String(v) : String(Math.round(v * 1e6) / 1e6);
  return v === null || v === undefined ? "" : String(v);
}

function esc(s: string): string {
  return (s || "").replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

function parseValue(raw: string, type: string): any {
  if (type === "string") return raw;
  if (raw === "true" || raw === "false") return raw === "true";
  if (raw.includes(".") || raw.toLowerCase().includes("e")) return parseFloat(raw);
  return parseInt(raw, 10);
}

// ---- 主题切换（暗色 / 亮色）----
function currentTheme(): Theme {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

function applyTheme(theme: Theme): void {
  if (theme === "light") document.documentElement.dataset.theme = "light";
  else delete document.documentElement.dataset.theme;
  const label = $("#themeLabel");
  if (label) label.textContent = theme === "light" ? "亮色" : "暗色";
  try {
    localStorage.setItem(THEME_KEY, theme);
  } catch {
    // WebView 禁用存储时忽略，主题仍可在本次会话内切换。
  }
}

function toggleTheme(): void {
  applyTheme(currentTheme() === "dark" ? "light" : "dark");
}

// ---- 左侧列表 ----
async function loadDbList(): Promise<void> {
  try {
    dbsList = await api.listDBs();
    const ul = $("#dbList");
    ul.innerHTML = "";
    dbsList.forEach((d) => {
      const li = document.createElement("li");
      if (d.number === selected) li.className = "active";
      li.innerHTML = `<span class="nm">DB${d.number} · ${esc(d.name || "未命名")}</span>
        <span class="mt">${d.size} 字节</span>`;
      li.onclick = () => void selectDB(d.number);
      li.oncontextmenu = (e: MouseEvent) => openCtx(e, d.number);
      ul.appendChild(li);
    });
  } catch (e: any) {
    toast("加载列表失败: " + e.message, false);
  }
}

async function selectDB(n: number): Promise<void> {
  selected = n;
  $("#qDb").value = n;
  await loadDbList();
  try {
    renderDetail(await api.getDB(n));
  } catch (e: any) {
    toast("查询失败: " + e.message, false);
  }
}

function refreshCurrent(): void {
  if (selected == null) return;
  const n = selected;
  api
    .getDB(n)
    .then((db) => renderDetail(db))
    .catch(() => undefined);
}

// ---- 右侧详情渲染 ----
function renderDetail(db: DBView): void {
  currentFields = (db.fields || []).slice();
  const fields = currentFields;
  const head = fields.length ? `<span class="badge">${fields.length} 字段</span>` : `<span class="badge">无字段</span>`;
  const dbName = db.name ? db.name : "（未命名）";
  const typeOptions = FIELD_TYPES.map((t) => `<option>${t}</option>`).join("");
  let html = `<div class="card"><h2>DB${db.number} ${head}
      <button class="sm ghost" title="从数据库重新加载此块" onclick="reloadDB(${db.number})">↻ 重新加载</button>
    </h2>
    <div class="query" style="margin:0 0 6px">
      <label>名称 <input id="dbName" value="${esc(dbName)}" style="width:200px"></label>
      <button class="ghost" onclick="renameDB(${db.number})">保存名称</button>
    </div>
    <div class="meta">大小 ${db.size} 字节 · 偏移量按字段顺序与类型自动计算（bool/bit 按位打包，多字节类型按字节对齐） · 双击「字段名 / 类型 / 值」列可直接修改</div>`;

  if (fields.length) {
    // table-layout:fixed + 固定列宽：行内编辑器（input/select）插入时列宽不再变化，
    // 避免「字段名 / 类型 / 值」双击编辑时页面瞬间变形。
    html += `<table class="fields"><colgroup>
      <col style="width:32px"><col><col style="width:90px"><col style="width:48px">
      <col style="width:36px"><col style="width:44px"><col><col style="width:168px">
    </colgroup><thead><tr>
      <th>#</th><th>字段名</th><th>类型</th><th>偏移</th><th>位</th><th>长度</th><th>值</th><th>操作</th>
      </tr></thead><tbody>`;
    fields.forEach((f, i) => {
      const isBit = f.type === "bool" || f.type === "bit";
      const raw = isBit ? String(!!f.value) : fmt(f.value);
      html += `<tr>
        <td class="mono">${i + 1}</td>
        <td class="mono edit-cell name-cell" title="双击修改字段名"
          data-db="${db.number}" data-name="${esc(f.name)}">${esc(f.name)}</td>
        <td class="edit-cell type-cell" title="双击修改类型"
          data-db="${db.number}" data-name="${esc(f.name)}">${f.type}</td>
        <td class="mono">${f.offset}</td>
        <td class="mono">${isBit ? f.bit : ""}</td>
        <td class="mono">${f.length || ""}</td>
        <td class="mono val-cell" title="双击修改当前值"
          data-db="${db.number}" data-name="${esc(f.name)}" data-type="${f.type}"
          data-offset="${f.offset}" data-bit="${f.bit}" data-raw="${esc(raw)}">${raw}</td>
        <td><div class="ops">
          <button class="sm ghost" onclick="moveField('${f.name}',-1)">↑</button>
          <button class="sm ghost" onclick="moveField('${f.name}',1)">↓</button>
          <button class="sm ghost" onclick="editField('${f.name}')">编辑</button>
          <button class="sm danger" onclick="deleteField('${f.name}')">删除</button>
        </div></td>
      </tr>`;
    });
    html += `</tbody></table>`;
  } else {
    html += `<div class="raw">${(db.raw || "").replace(/(.{2})/g, "$1 ").trim() || "（空）"}</div>
      <div class="hint">该 DB 尚未配置字段，可在下方设置字段定义。</div>`;
  }

  html += `<div class="fieldcfg">
    <strong>配置字段</strong>
    <span class="meta">填写名称与类型即可，偏移/位号由系统按字段顺序与类型自动计算并持久化（重启保留）。</span>
    <div class="add-form">
      <label>字段名<input id="fName" placeholder="如 MotorOn"></label>
      <label>类型<select id="fType">${typeOptions}</select></label>
      <label>长度(string)<input id="fLen" type="number" value="0" style="width:100%"></label>
      <label>初始值<input id="fValue" placeholder="bool:true 数值:12.5 串:abc"></label>
      <div style="display:flex;gap:8px;align-items:end">
        <button class="green" id="fSave" onclick="submitField()">添加字段</button>
        <button class="ghost" id="fCancel" onclick="cancelEdit()" style="display:none">取消</button>
      </div>
    </div>
  </div></div>`;
  $("#detail").innerHTML = html;
}

function editField(name: string): void {
  const f = currentFields.find((x) => x.name === name);
  if (!f) return;
  $("#fName").value = f.name;
  $("#fType").value = f.type;
  $("#fLen").value = f.length || 0;
  $("#fValue").value = f.value === undefined || f.value === null ? "" : fmt(f.value);
  editingName = name;
  $("#fSave").textContent = "保存修改";
  $("#fCancel").style.display = "";
  $("#fName").focus();
}

function cancelEdit(): void {
  editingName = null;
  clearForm();
}

function clearForm(): void {
  $("#fName").value = "";
  $("#fType").value = "bool";
  $("#fLen").value = 0;
  $("#fValue").value = "";
  $("#fSave").textContent = "添加字段";
  $("#fCancel").style.display = "none";
}

// ---- 字段名 / 类型 / 值的双击编辑 ----
async function saveFieldMeta(
  dbNum: number,
  oldName: string,
  patch: { name?: string; type?: string; length?: number; value?: any },
): Promise<void> {
  const f = currentFields.find((x) => x.name === oldName);
  if (!f) return;
  const name = (patch.name ?? f.name).trim();
  const type = patch.type ?? f.type;
  if (!name) {
    toast("字段名不能为空", false);
    return;
  }
  if (name === oldName && type === f.type) return;

  let ord = 0;
  let deleted = false;
  try {
    // 改名需删除旧定义再新增；保留 ord 以维持字段顺序。
    const stored = await api.listFields(dbNum);
    ord = stored.find((x) => x.name === oldName)?.ord ?? 0;
    if (name !== oldName) {
      await api.deleteField(dbNum, oldName);
      deleted = true;
    }
    // offset/bit 由后端按字段顺序与类型重新计算，这里补占位值满足传输类型。
    await api.upsertField(dbNum, {
      name,
      type,
      offset: 0,
      bit: 0,
      length: patch.length ?? f.length ?? 0,
      ord,
      value: patch.value !== undefined ? patch.value : f.value,
    });
    toast(`DB${dbNum} 字段 ${oldName} 已改为 ${name}（${type}）`);
  } catch (e: any) {
    if (deleted) {
      // 回滚：尽力恢复原字段定义，避免改名失败后字段丢失。
      try {
        await api.upsertField(dbNum, {
          name: oldName,
          type: f.type,
          offset: 0,
          bit: 0,
          length: f.length ?? 0,
          ord,
          value: f.value,
        });
      } catch {
        // 回滚失败时由用户手动重新加载。
      }
    }
    toast("保存失败: " + e.message, false);
  }
  refreshCurrent();
}

function beginNameEdit(td: HTMLElement): void {
  if (td.querySelector("input")) return;
  const dbNum = Number(td.dataset.db);
  const oldName = td.dataset.name || "";
  const original = td.textContent || oldName;

  const input = document.createElement("input");
  input.type = "text";
  input.value = oldName;
  let done = false;
  const finish = async (commit: boolean): Promise<void> => {
    if (done) return;
    done = true;
    input.onblur = null;
    input.onkeydown = null;
    const next = input.value.trim();
    if (!commit || !next || next === oldName) {
      td.textContent = original;
      return;
    }
    td.textContent = next;
    await saveFieldMeta(dbNum, oldName, { name: next });
  };
  input.onblur = () => void finish(true);
  input.onkeydown = (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void finish(true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      void finish(false);
    }
  };

  td.textContent = "";
  td.appendChild(input);
  input.focus();
  input.select();
}

function beginTypeEdit(td: HTMLElement): void {
  if (td.querySelector("select")) return;
  const dbNum = Number(td.dataset.db);
  const oldName = td.dataset.name || "";
  const original = td.textContent || "";

  const select = document.createElement("select");
  FIELD_TYPES.forEach((t) => {
    const option = document.createElement("option");
    option.value = t;
    option.textContent = t;
    option.selected = t === original;
    select.appendChild(option);
  });
  let done = false;
  const finish = async (): Promise<void> => {
    if (done) return;
    done = true;
    select.onchange = null;
    select.onblur = null;
    const next = select.value;
    if (next === original) {
      td.textContent = original;
      return;
    }
    td.textContent = next;
    await saveFieldMeta(dbNum, oldName, { type: next });
  };
  select.onchange = () => void finish();
  select.onblur = () => void finish();

  td.textContent = "";
  td.appendChild(select);
  select.focus();
}

function beginValueEdit(td: HTMLElement): void {
  if (td.querySelector("input")) return;

  const dbNum = Number(td.dataset.db);
  const name = td.dataset.name || "";
  const type = td.dataset.type || "";
  const offset = Number(td.dataset.offset);
  const bit = Number(td.dataset.bit);
  const isBit = type === "bool" || type === "bit";
  const raw = td.dataset.raw ?? td.textContent ?? "";
  const original = td.textContent ?? "";

  const input = document.createElement("input");
  input.type = isBit ? "checkbox" : "text";
  if (isBit) input.checked = raw === "true";
  else input.value = raw;

  let done = false;
  const finish = async (commit: boolean): Promise<void> => {
    if (done) return;
    done = true;
    input.onblur = null;
    input.onchange = null;
    input.onkeydown = null;

    let display = original;
    let value: any;
    if (isBit) {
      value = input.checked;
      display = String(value);
    } else {
      const text = input.value.trim();
      if (text === "" && type !== "string") {
        td.textContent = original;
        return;
      }
      value = parseValue(text, type);
      if (typeof value === "number" && Number.isNaN(value)) {
        toast("数值格式无效", false);
        td.textContent = original;
        return;
      }
      display = fmt(value);
    }
    td.textContent = display;
    td.dataset.raw = display;
    if (!commit) return;

    try {
      if (isBit) await api.setBit(dbNum, offset, bit, value);
      else await api.setField(dbNum, name, value);
      toast(`DB${dbNum} 字段 ${name} = ${display}`);
    } catch (e: any) {
      toast("写入失败: " + e.message, false);
    }
    refreshCurrent();
  };

  input.onblur = () => void finish(true);
  input.onchange = () => void finish(true);
  input.onkeydown = (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      void finish(true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      void finish(false);
    }
  };

  td.textContent = "";
  td.appendChild(input);
  input.focus();
  if (!isBit) input.select();
}

async function moveField(name: string, dir: number): Promise<void> {
  if (selected == null) {
    toast("请先选择数据块", false);
    return;
  }
  try {
    await api.moveField(selected, name, dir);
    refreshCurrent();
  } catch (e: any) {
    toast("移动失败: " + e.message, false);
  }
}

async function deleteField(name: string): Promise<void> {
  if (selected == null) {
    toast("请先选择数据块", false);
    return;
  }
  try {
    await api.deleteField(selected, name);
    toast(`DB${selected} 字段 ${name} 已删除`);
    refreshCurrent();
  } catch (e: any) {
    toast("删除失败: " + e.message, false);
  }
}

async function submitField(): Promise<void> {
  if (selected == null) {
    toast("请先选择数据块", false);
    return;
  }
  const name = String($("#fName").value).trim();
  if (!name) {
    toast("请填写字段名称", false);
    return;
  }
  const type = String($("#fType").value);
  const field: any = { name, type, length: parseInt(String($("#fLen").value || "0"), 10) };
  const raw = String($("#fValue").value).trim();
  if (raw !== "") field.value = parseValue(raw, type);
  try {
    if (editingName && editingName !== name) {
      await api.deleteField(selected, editingName);
    }
    await api.upsertField(selected, field);
    toast(`DB${selected} 字段 ${name} 已保存`);
    editingName = null;
    clearForm();
    refreshCurrent();
  } catch (e: any) {
    toast("保存失败: " + e.message, false);
  }
}

async function renameDB(num: number): Promise<void> {
  const name = String($("#dbName").value).trim();
  try {
    await api.renameDB(num, name);
    toast(`DB${num} 名称已保存`);
    await loadDbList();
  } catch (e: any) {
    toast("保存名称失败: " + e.message, false);
  }
}

// ---- 右键菜单 ----
function openCtx(e: MouseEvent, n: number): void {
  e.preventDefault();
  ctxNum = n;
  const m = $("#ctx");
  m.style.display = "block";
  m.style.left = Math.min(e.clientX, innerWidth - 140) + "px";
  m.style.top = Math.min(e.clientY, innerHeight - 120) + "px";
}

function hideCtx(): void {
  $("#ctx").style.display = "none";
}

function ctxRename(): void {
  const n = ctxNum;
  hideCtx();
  if (n != null) showRename(n);
}

function ctxClone(): void {
  const n = ctxNum;
  hideCtx();
  if (n != null) showClone(n);
}

async function ctxDelete(): Promise<void> {
  const n = ctxNum;
  hideCtx();
  if (n == null) return;
  if (!confirm(`确认删除 DB${n}？该数据块及其字段定义将被移除。`)) return;
  try {
    await api.deleteDB(n);
    toast(`已删除 DB${n}`);
    if (selected === n) {
      selected = null;
      $("#detail").innerHTML = '<div class="card"><div class="empty">请选择左侧数据块</div></div>';
    }
    await loadDbList();
  } catch (e: any) {
    toast("删除失败: " + e.message, false);
  }
}

document.addEventListener("click", (e) => {
  const target = e.target as HTMLElement;
  if (!target.closest("#ctx") && !target.closest(".db-list li")) hideCtx();
});

// ---- 弹窗（新建 / 重命名 / 复制）----
function showModal(title: string, bodyHTML: string, onOk: () => void): void {
  $("#modalTitle").textContent = title;
  $("#modalBody").innerHTML = bodyHTML;
  $("#modalOk").onclick = onOk;
  $("#modal").style.display = "flex";
}

function closeModal(): void {
  $("#modal").style.display = "none";
}

function showCreate(): void {
  modalCtx = null;
  showModal(
    "新建数据块",
    `<label>DB 号 <input id="mNum" type="number" placeholder="如 200"></label>
    <label>名称 <input id="mName" placeholder="可选"></label>
    <label>大小(字节) <input id="mSize" type="number" value="128"></label>`,
    () => void okCreate(),
  );
  setTimeout(() => $("#mNum").focus(), 30);
}

async function okCreate(): Promise<void> {
  const num = parseInt(String($("#mNum").value), 10);
  const size = parseInt(String($("#mSize").value), 10);
  const name = String($("#mName").value).trim();
  if (!num || !size) {
    toast("请填写 DB 号与大小", false);
    return;
  }
  try {
    await api.createDB(num, size, name);
    closeModal();
    toast(`DB${num} 已创建`);
    await loadDbList();
    void selectDB(num);
  } catch (e: any) {
    toast("创建失败: " + e.message, false);
  }
}

function showRename(n: number): void {
  modalCtx = n;
  const d = dbsList.find((x) => x.number === n);
  const cur = d ? d.name : "";
  showModal("重命名 DB" + n, `<label>名称 <input id="mName" value="${esc(cur)}"></label>`, () => void okRename());
  setTimeout(() => $("#mName").focus(), 30);
}

async function okRename(): Promise<void> {
  const n = modalCtx;
  if (n == null) return;
  const name = String($("#mName").value).trim();
  try {
    await api.renameDB(n, name);
    closeModal();
    toast("名称已保存");
    await loadDbList();
    if (selected === n) void selectDB(n);
  } catch (e: any) {
    toast("保存失败: " + e.message, false);
  }
}

function showClone(n: number): void {
  modalCtx = n;
  const d = dbsList.find((x) => x.number === n);
  const cur = d ? d.name : "";
  showModal(
    "复制 DB" + n + " 为",
    `<label>新 DB 号 <input id="mNum" type="number" placeholder="如 201"></label>
    <label>新名称 <input id="mName" value="${esc(cur ? cur + "副本" : "")}"></label>`,
    () => void okClone(),
  );
  setTimeout(() => $("#mNum").focus(), 30);
}

async function okClone(): Promise<void> {
  const src = modalCtx;
  if (src == null) return;
  const num = parseInt(String($("#mNum").value), 10);
  const name = String($("#mName").value).trim();
  if (!num) {
    toast("请填写新 DB 号", false);
    return;
  }
  try {
    await api.cloneDB(src, num, name, 0);
    closeModal();
    toast(`已复制为 DB${num}`);
    await loadDbList();
    void selectDB(num);
  } catch (e: any) {
    toast("复制失败: " + e.message, false);
  }
}

// ---- 从数据库重新加载 ----
async function reloadDB(n: number): Promise<void> {
  try {
    await api.reloadDB(n);
    toast(`DB${n} 已从数据库重新加载`);
    await loadDbList();
    void selectDB(n);
  } catch (e: any) {
    toast("重新加载失败: " + e.message, false);
  }
}

async function reloadAll(): Promise<void> {
  try {
    await api.reloadAll();
    toast("已全部从数据库重新加载");
    await loadDbList();
    if (selected != null) void selectDB(selected);
  } catch (e: any) {
    toast("重新加载失败: " + e.message, false);
  }
}

// ---- 导出 / 导入 ----
function downloadJSON(filename: string, data: any): void {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}

async function exportDB(number: number): Promise<void> {
  try {
    const bundle = await api.exportDB(number);
    downloadJSON(`plc-simulater-db${number}.json`, bundle);
    toast(`DB${number} 已导出`);
  } catch (e: any) {
    toast("导出失败: " + e.message, false);
  }
}

async function exportAllDBs(): Promise<void> {
  try {
    const bundle = await api.exportAll();
    const stamp = new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-");
    downloadJSON(`plc-simulater-dbs-${stamp}.json`, bundle);
    toast(`已导出 ${bundle.dbs?.length ?? 0} 个数据块`);
  } catch (e: any) {
    toast("导出失败: " + e.message, false);
  }
}

function triggerImport(): void {
  $("#importFile").click();
}

// 兼容两种文件内容：整体导出包 {version, dbs:[...]} 或单块导出 {number, ...}。
function normalizeBundle(parsed: any): ExportBundle | null {
  if (parsed && Array.isArray(parsed.dbs) && parsed.dbs.length > 0) {
    return { version: parsed.version ?? "", exportedAt: parsed.exportedAt, dbs: parsed.dbs };
  }
  if (parsed && typeof parsed.number === "number") {
    return { version: parsed.version ?? "", exportedAt: parsed.exportedAt, dbs: [parsed] };
  }
  return null;
}

async function handleImportFile(ev: Event): Promise<void> {
  const input = ev.target as HTMLInputElement;
  const file = input.files && input.files[0];
  input.value = ""; // 允许重复选择同一文件
  if (!file) return;

  let parsed: any;
  try {
    parsed = JSON.parse(await file.text());
  } catch {
    toast("导入文件不是有效的 JSON", false);
    return;
  }
  const bundle = normalizeBundle(parsed);
  if (!bundle) {
    toast("导入文件格式不正确：缺少 dbs 数组或单块 number 字段", false);
    return;
  }

  const total = bundle.dbs.length;
  if (!confirm(`确认导入 ${total} 个数据块？同号数据块将被覆盖（含字段定义与当前值）。`)) return;

  let done = 0;
  let failMsg = "";
  try {
    done = await api.importAll(bundle);
  } catch (e: any) {
    failMsg = e.message;
  }
  await loadDbList();
  if (selected != null) {
    if (dbsList.some((x) => x.number === selected)) void selectDB(selected);
    else {
      selected = null;
      $("#detail").innerHTML = '<div class="card"><div class="empty">请选择左侧数据块</div></div>';
    }
  }
  if (failMsg) toast(`导入中止：已导入 ${done} 个，${failMsg}`, false);
  else toast(`已导入 ${done} 个数据块`);
}

function ctxExport(): void {
  const n = ctxNum;
  hideCtx();
  if (n != null) void exportDB(n);
}

// ---- 原始查询 ----
async function rawQuery(): Promise<void> {
  const db = parseInt(String($("#qDb").value), 10);
  const off = parseInt(String($("#qOff").value), 10);
  const len = parseInt(String($("#qLen").value), 10);
  try {
    const d = await api.rawQuery(db, off, len);
    $("#rawOut").textContent = d.hex.replace(/(.{2})/g, "$1 ").trim();
  } catch (e: any) {
    toast("查询失败: " + e.message, false);
  }
}

function toggleAuto(): void {
  const s = $("#auto");
  if (timer) {
    clearInterval(timer);
    timer = null;
    s.textContent = "关";
  } else {
    timer = window.setInterval(refreshCurrent, 2000);
    s.textContent = "开(2s)";
  }
}

// index.html 中的内联事件处理器需要这些函数在全局作用域可见。
Object.assign(window, {
  toggleTheme,
  toggleAuto,
  showCreate,
  reloadAll,
  exportAllDBs,
  triggerImport,
  ctxExport,
  moveField,
  deleteField,
  submitField,
  cancelEdit,
  editField,
  renameDB,
  ctxRename,
  ctxClone,
  ctxDelete,
  closeModal,
  reloadDB,
  rawQuery,
});

// 单元格采用事件委托：renderDetail 会替换 #detail 内部内容，但容器本身保持不变。
$("#detail").addEventListener("dblclick", (e: MouseEvent) => {
  const cell = (e.target as HTMLElement).closest<HTMLElement>(".val-cell, .name-cell, .type-cell");
  if (!cell) return;
  if (cell.classList.contains("val-cell")) beginValueEdit(cell);
  else if (cell.classList.contains("name-cell")) beginNameEdit(cell);
  else if (cell.classList.contains("type-cell")) beginTypeEdit(cell);
});

(async function init() {
  applyTheme(currentTheme());
  $("#importFile").addEventListener("change", (e: Event) => void handleImportFile(e));
  $("#modeBadge").textContent = runtimeMode === "gui" ? "GUI · Wails 3" : "Web · HTTP RPC";
  await loadDbList();
  if (dbsList.length) void selectDB(dbsList[0].number);
})();
