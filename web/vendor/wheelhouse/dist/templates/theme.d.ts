import { Severity, Threshold, WidgetTheme } from '../types';
export declare const DEFAULT_WIDGET_THEME: WidgetTheme;
export declare function resolveTheme(theme?: Partial<WidgetTheme>): WidgetTheme;
export declare function severityColor(severity: Severity, theme: WidgetTheme): string;
export declare function activeSeverity(value: number, thresholds: readonly Threshold[]): Severity | undefined;
//# sourceMappingURL=theme.d.ts.map