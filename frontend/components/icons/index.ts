import Alert from "./Alert";
import CalendarCheck from "./CalendarCheck";
import Check from "./Check";
import Chevron from "./Chevron";
import CriticalPolicy from "./CriticalPolicy";
import DownCaret from "./DownCaret";
import Ex from "./Ex";
import EmptyHosts from "./EmptyHosts";
import EmptyIntegrations from "./EmptyIntegrations";
import EmptyMembers from "./EmptyMembers";
import EmptyPacks from "./EmptyPacks";
import EmptyPolicies from "./EmptyPolicies";
import EmptyQueries from "./EmptyQueries";
import EmptySchedule from "./EmptySchedule";
import EmptySoftware from "./EmptySoftware";
import EmptyTeams from "./EmptyTeams";
import ExternalLink from "./ExternalLink";
import Issue from "./Issue";
import Plus from "./Plus";
import PremiumFeature from "./PremiumFeature";

import LowDiskSpaceHosts from "./LowDiskSpaceHosts";
import MissingHosts from "./MissingHosts";
import Lightbulb from "./Lightbulb";

import Apple from "./Apple";
import Windows from "./Windows";
import Linux from "./Linux";
import M1 from "./M1";
import Centos from "./Centos";
import Ubuntu from "./Ubuntu";

// Encircled
import ApplePurple from "./ApplePurple";
import LinuxGreen from "./LinuxGreen";
import WindowsBlue from "./WindowsBlue";

// Status Icons
import Success from "./Success";
import SuccessPartial from "./SuccessPartial";
import Pending from "./Pending";
import PendingPartial from "./PendingPartial";
import Error from "./Error";

import Clipboard from "./Clipboard";
import Eye from "./Eye";
import Pencil from "./Pencil";
import TrashCan from "./TrashCan";
import Profile from "./Profile";
import Download from "./Download";
import Files from "./Files";
import Refresh from "./Refresh";
import FilePython from "./FilePython";
import FileZsh from "./FileZsh";
import FileBash from "./FileBash";
import FileGeneric from "./FileGeneric";
import FilePkg from "./FilePkg";

// a mapping of the usable names of icons to the icon source.
export const ICON_MAP = {
  alert: Alert,
  "calendar-check": CalendarCheck,
  chevron: Chevron,
  check: Check,
  "critical-policy": CriticalPolicy,
  "down-caret": DownCaret,
  ex: Ex,
  "empty-hosts": EmptyHosts,
  "empty-integrations": EmptyIntegrations,
  "empty-members": EmptyMembers,
  "empty-packs": EmptyPacks,
  "empty-policies": EmptyPolicies,
  "empty-queries": EmptyQueries,
  "empty-schedule": EmptySchedule,
  "empty-software": EmptySoftware,
  "empty-teams": EmptyTeams,
  "external-link": ExternalLink,
  "low-disk-space-hosts": LowDiskSpaceHosts,
  "missing-hosts": MissingHosts,
  lightbulb: Lightbulb,
  issue: Issue,
  plus: Plus,
  clipboard: Clipboard,
  eye: Eye,
  pencil: Pencil,
  trash: TrashCan,
  success: Success,
  "success-partial": SuccessPartial,
  pending: Pending,
  "pending-partial": PendingPartial,
  error: Error,
  darwin: Apple,
  macOS: Apple,
  windows: Windows,
  Windows,
  linux: Linux,
  Linux,
  m1: M1,
  centos: Centos,
  ubuntu: Ubuntu,
  "premium-feature": PremiumFeature,
  "darwin-purple": ApplePurple,
  "windows-blue": WindowsBlue,
  "linux-green": LinuxGreen,
  profile: Profile,
  download: Download,
  files: Files,
  "file-python": FilePython,
  "file-zsh": FileZsh,
  "file-bash": FileBash,
  "file-pkg": FilePkg,
  "file-generic": FileGeneric,
  refresh: Refresh,
};

export type IconNames = keyof typeof ICON_MAP;
