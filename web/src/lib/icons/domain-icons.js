import {
  AlertTriangle as IconAlertTriangle,
  Bell as IconBell,
  Zap as IconBolt,
  Brain as IconBrain,
  Building2 as IconBuildingCommunity,
  Check as IconCheck,
  CircleCheck as IconCircleCheck,
  CircleX as IconCircleX,
  ClipboardList as IconClipboardList,
  Clock4 as IconClockHour4,
  Camera as IconCamera,
  Copy as IconCopy,
  Cpu as IconCpu,
  Database as IconDatabase,
  Pencil as IconEdit,
  File as IconFile,
  FileText as IconFileText,
  FileType as IconFileTypePdf,
  Fingerprint as IconFingerprint,
  Flower as IconFlower,
  Folder as IconFolder,
  Gauge as IconGauge,
  GitBranch as IconGitBranch,
  Inbox as IconInbox,
  Info as IconInfoCircle,
  FileCode as IconJson,
  LogIn as IconLogin2,
  Moon as IconMoon,
  Music as IconMusic,
  Network as IconNetwork,
  Package as IconPackage,
  Palette as IconPalette,
  PenLine as IconPencil,
  Image as IconPhoto,
  Sprout as IconPlant,
  Plug as IconPlugConnected,
  Loader as IconProgress,
  CircleHelp as IconQuestionMark,
  Receipt as IconReceipt,
  Bot as IconRobot,
  Rocket as IconRocket,
  RotateCw as IconRotateClockwise,
  Search as IconSearch,
  Server as IconServer,
  Settings as IconSettings,
  ShieldCheck as IconShieldCheck,
  ShieldAlert as IconShieldLock,
  PenLine as IconSignature,
  Sparkles as IconSparkles,
  Sun as IconSun,
  Target as IconTarget,
  Video as IconVideo,
  X as IconX
} from '@lucide/svelte';

export const ServiceIcon = IconServer;
export const ArtifactIcon = IconPackage;
export const EnvironmentIcon = IconTarget;
export const DeploymentIcon = IconRocket;
export const NotificationIcon = IconBell;
export const PaymentIcon = IconReceipt;
export const PolicyIcon = IconShieldCheck;
export const OrganizationIcon = IconBuildingCommunity;
export const LlmIcon = IconBrain;
export const AppearanceIcon = IconPalette;
export const RollbackIcon = IconRotateClockwise;
export const BlossomIcon = IconFlower;
export const ProtectedIcon = IconShieldLock;
export const WarningIcon = IconAlertTriangle;
export const SuccessIcon = IconCircleCheck;
export const ErrorIcon = IconCircleX;
export const InfoIcon = IconInfoCircle;
export const UnknownIcon = IconQuestionMark;
export const CopyIcon = IconCopy;
export const CloseIcon = IconX;
export const CheckIcon = IconCheck;
export const SbomIcon = IconClipboardList;
export const SignatureIcon = IconSignature;
export const EmptyIcon = IconInbox;
export const SunIcon = IconSun;
export const MoonIcon = IconMoon;
export const LoginIcon = IconLogin2;
export const SearchIcon = IconSearch;
export const EditIcon = IconEdit;
export const PendingIcon = IconClockHour4;
export const CameraIcon = IconCamera;
export const RelayIcon = IconPlugConnected;
export const ConfiguredIcon = IconSettings;
export const RepositoryIcon = IconGitBranch;
export const SoulIcon = IconBrain;
export const IdentityIcon = IconFingerprint;
export const AvatarIcon = IconPalette;
export const ProfileIcon = IconPencil;
export const MemoryIcon = IconDatabase;
export const SeedIcon = IconPlant;
export const WorkspaceIcon = IconFolder;
export const ProgressIcon = IconProgress;
export const TemplateIcon = IconSparkles;
export const LightweightIcon = IconBolt;
export const StandardIcon = IconRobot;
export const HeavyIcon = IconGauge;
export const MLFabricIcon = IconNetwork;
export const AcceleratorIcon = IconCpu;

export const ImageFileIcon = IconPhoto;
export const VideoFileIcon = IconVideo;
export const AudioFileIcon = IconMusic;
export const JsonFileIcon = IconJson;
export const TextFileIcon = IconFileText;
export const PdfFileIcon = IconFileTypePdf;
export const GenericFileIcon = IconFile;

export function blossomContentTypeIcon(contentType) {
  const normalized = (contentType || '').toLowerCase();

  if (normalized.startsWith('image/')) return ImageFileIcon;
  if (normalized.startsWith('video/')) return VideoFileIcon;
  if (normalized.startsWith('audio/')) return AudioFileIcon;
  if (normalized.includes('json')) return JsonFileIcon;
  if (normalized.startsWith('text/') || normalized.includes('text')) return TextFileIcon;
  if (normalized.includes('pdf')) return PdfFileIcon;

  return GenericFileIcon;
}
