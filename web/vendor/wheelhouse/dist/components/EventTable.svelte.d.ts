import { Component } from 'svelte';
import { EventTableData, WidgetMeta } from '../types';
declare const EventTable: Component<{ data: EventTableData; meta: WidgetMeta }>;
export default EventTable;
