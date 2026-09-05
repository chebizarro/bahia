import { EChartsOption } from 'echarts';
import { InlineWidgetModel, TemplateDefinition, TemplateName, WidgetTheme } from '../types';
export declare const templateRegistry: Readonly<Record<TemplateName, TemplateDefinition>>;
export declare function getTemplate(name: TemplateName): TemplateDefinition;
export declare function buildEChartsOption(model: InlineWidgetModel, theme?: Partial<WidgetTheme>): EChartsOption;
//# sourceMappingURL=registry.d.ts.map