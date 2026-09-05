import { Component } from 'svelte';
import { WidgetModel, WidgetTheme } from '../types';
declare const WidgetCard: Component<{ model: WidgetModel; theme?: Partial<WidgetTheme> }>;
export default WidgetCard;
