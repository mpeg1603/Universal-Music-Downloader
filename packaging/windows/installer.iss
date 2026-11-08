; The Windows installer of the window app (Inno Setup). The workflow passes:
;   /DAppVersion=1.2.3 /DSourceDir=<folder with the app and its Qt libraries> /DOutputDir=<where to put it>
#define AppName "Universal Music Downloader"
#define AppExe "universal-music-downloader-gui.exe"

[Setup]
AppId={{7C1E3C5B-3E4B-4D35-9E1E-2B6F0A5D7A11}
AppName={#AppName}
AppVersion={#AppVersion}
AppPublisher=Universal Music Downloader contributors
DefaultDirName={autopf}\{#AppName}
DefaultGroupName={#AppName}
DisableProgramGroupPage=yes
OutputDir={#OutputDir}
OutputBaseFilename=universal-music-downloader-gui-{#AppVersion}-windows-amd64-setup
SetupIconFile=..\icon.ico
UninstallDisplayIcon={app}\{#AppExe}
Compression=lzma2
SolidCompression=yes
WizardStyle=modern
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequiredOverridesAllowed=dialog
LicenseFile=..\..\LICENSE

[Tasks]
Name: "desktopicon"; Description: "{cm:CreateDesktopIcon}"; GroupDescription: "{cm:AdditionalIcons}"

[Files]
Source: "{#SourceDir}\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\{#AppName}"; Filename: "{app}\{#AppExe}"
Name: "{autodesktop}\{#AppName}"; Filename: "{app}\{#AppExe}"; Tasks: desktopicon

[Run]
Filename: "{app}\{#AppExe}"; Description: "{cm:LaunchProgram,{#AppName}}"; Flags: nowait postinstall skipifsilent
