import { EChartsOption } from 'echarts';
import { TEMPLATE_NAMES, WIDGET_KINDS } from './constants';
export type WidgetKind = (typeof WIDGET_KINDS)[number];
export type TemplateName = (typeof TEMPLATE_NAMES)[number];
export type Severity = 'info' | 'warn' | 'critical';
export type ThresholdOperator = '>' | '>=' | '<' | '<=' | '==';
export interface WidgetMeta {
    title: string;
    alt: string;
    subtitle?: string;
    unit?: string;
    [key: string]: unknown;
}
export interface WidgetScope {
    metric: string;
    host: string;
    service: string;
    [key: string]: unknown;
}
export interface WidgetQuery {
    from: number;
    to: number;
    step: number;
    generated_at: number;
    staleness_ttl: number;
    window: string;
    [key: string]: unknown;
}
export interface Threshold {
    op: ThresholdOperator;
    value: number;
    severity: Severity;
    label?: string;
    [key: string]: unknown;
}
export interface TimeseriesData {
    timestamps: number[];
    series: Array<{
        name: string;
        values: Array<number | null>;
        [key: string]: unknown;
    }>;
    [key: string]: unknown;
}
export interface GaugeData {
    value: number;
    min: number;
    max: number;
    [key: string]: unknown;
}
export interface StatData {
    value: number | string;
    label?: string;
    change?: number;
    [key: string]: unknown;
}
export type TableCell = string | number | boolean | null;
export interface EventTableData {
    columns: string[];
    rows: TableCell[][];
    [key: string]: unknown;
}
export type WidgetData = TimeseriesData | GaugeData | StatData | EventTableData;
export interface WidgetDataRef {
    url: string;
    sha256: string;
    media_type?: 'application/json';
    [key: string]: unknown;
}
export interface WidgetPresentation {
    template: TemplateName;
    thresholds: Threshold[];
    [key: string]: unknown;
}
export interface DashboardWidgetEnvelope {
    type: 'dashboard_widget';
    version: 1;
    widget_kind: WidgetKind;
    meta: WidgetMeta;
    scope: WidgetScope;
    query: WidgetQuery;
    presentation: WidgetPresentation;
    data?: WidgetData;
    data_ref?: WidgetDataRef;
    [key: string]: unknown;
}
export interface NostrWidgetEvent {
    id: string;
    pubkey: string;
    created_at: number;
    kind: number;
    tags: string[][];
    content: string;
    sig?: string;
}
export interface WidgetModelBase {
    eventId: string;
    pubkey: string;
    createdAt: number;
    dTag: string;
    tags: Readonly<Record<string, readonly string[]>>;
    envelope: DashboardWidgetEnvelope;
}
export interface InlineWidgetModel extends WidgetModelBase {
    source: 'inline';
    data: WidgetData;
}
export interface DataRefWidgetModel extends WidgetModelBase {
    source: 'data_ref';
    dataRef: WidgetDataRef;
}
export type WidgetModel = InlineWidgetModel | DataRefWidgetModel;
export type WidgetRejectionCode = 'invalid_event' | 'wrong_kind' | 'missing_d_tag' | 'invalid_d_tag' | 'oversized_content' | 'invalid_json' | 'invalid_envelope' | 'unsupported_version' | 'unsupported_widget_kind' | 'unsupported_template' | 'template_mismatch' | 'invalid_data' | 'cap_exceeded';
export interface FallbackMetadata {
    title: string;
    alt: string;
    tags: Readonly<Record<string, readonly string[]>>;
}
export interface WidgetRejection {
    code: WidgetRejectionCode;
    message: string;
    issues?: readonly string[];
    fallback: FallbackMetadata;
}
export type WidgetValidationResult = {
    ok: true;
    model: WidgetModel;
} | {
    ok: false;
    rejection: WidgetRejection;
};
export interface WidgetTheme {
    text: string;
    muted: string;
    info: string;
    warn: string;
    critical: string;
}
export interface TemplateDefinition {
    name: TemplateName;
    widgetKind: WidgetKind;
    renderTarget: 'echarts' | 'table';
    buildOption?: (model: InlineWidgetModel, theme?: Partial<WidgetTheme>) => EChartsOption;
}
/**
 * Future sidecar integrations implement this boundary and MUST verify ref.sha256
 * before returning parsed data. Wheelhouse then re-validates the returned data.
 */
export interface VerifiedSidecarFetcher {
    fetchVerified(ref: WidgetDataRef): Promise<unknown>;
}
//# sourceMappingURL=types.d.ts.map