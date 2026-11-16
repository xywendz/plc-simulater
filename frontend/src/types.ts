export interface FieldView {
  name: string;
  offset: number;
  type: string;
  bit: number;
  length: number;
  value: any;
}

export interface DBView {
  number: number;
  name: string;
  size: number;
  fields: FieldView[];
  raw: string;
}

export interface FieldMeta {
  name: string;
  offset: number;
  type: string;
  bit: number;
  length: number;
  ord: number;
  value?: any;
}

export interface RawResult {
  db: number;
  offset: number;
  hex: string;
}

export interface AppInfo {
  version: string;
  mode: string;
  listen: string;
  max_pdu: number;
}

export interface ExportedField {
  name: string;
  type: string;
  offset?: number;
  bit?: number;
  length?: number;
  value?: any;
}

export interface ExportedDB {
  number: number;
  name?: string;
  size: number;
  fields?: ExportedField[];
  raw?: string;
}

export interface ExportBundle {
  version: string;
  exportedAt?: string;
  dbs: ExportedDB[];
}
