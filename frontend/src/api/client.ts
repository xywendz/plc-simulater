import { raw } from "@/runtime";
import type { AppInfo, DBView, ExportBundle, FieldMeta, RawResult } from "../types";

async function call<T>(p: Promise<T>): Promise<T> {
  return await p;
}

export const info = () => call<AppInfo>(raw.Info());
export const listDBs = () => call<DBView[]>(raw.ListDBs());
export const getDB = (number: number) => call<DBView>(raw.GetDB(number));
export const listFields = (number: number) => call<FieldMeta[]>(raw.ListFields(number));
export const createDB = (number: number, size: number, name: string) =>
  call<void>(raw.CreateDB(number, size, name));
export const cloneDB = (src: number, dst: number, name: string, size: number) =>
  call<void>(raw.CloneDB(src, dst, name, size));
export const renameDB = (number: number, name: string) => call<void>(raw.RenameDB(number, name));
export const deleteDB = (number: number) => call<void>(raw.DeleteDB(number));
export const reloadDB = (number: number) => call<void>(raw.ReloadDB(number));
export const reloadAll = () => call<void>(raw.ReloadAll());
export const setBit = (number: number, offset: number, bit: number, value: boolean) =>
  call<void>(raw.SetBit(number, offset, bit, value));
export const setField = (number: number, name: string, value: any) =>
  call<void>(raw.SetField(number, name, value));
export const upsertField = (number: number, field: FieldMeta) =>
  call<void>(raw.UpsertField(number, field));
export const deleteField = (number: number, name: string) =>
  call<void>(raw.DeleteField(number, name));
export const moveField = (number: number, name: string, dir: number) =>
  call<void>(raw.MoveField(number, name, dir));
export const rawQuery = (number: number, offset: number, length: number) =>
  call<RawResult>(raw.RawQuery(number, offset, length));
export const exportDB = (number: number) => call<ExportBundle>(raw.ExportDB(number));
export const exportAll = () => call<ExportBundle>(raw.ExportAll());
export const importDB = (db: any) => call<void>(raw.ImportDB(db));
export const importAll = (bundle: ExportBundle) => call<number>(raw.ImportAll(bundle));
