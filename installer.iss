; Script do instalador (Inno Setup 6). O GitHub Actions compila este arquivo
; e gera o SnowDownloader-Setup.exe. Instala só para o usuário atual, então
; não pede senha de administrador e o programa consegue se atualizar sozinho.

#define MyAppName "SnowDownloader"
#ifndef AppVersion
  #define AppVersion "1.0"
#endif

[Setup]
AppId={{B7C3C1E2-5A41-4D0B-9C57-5E0F6E2A7D11}
AppName={#MyAppName}
AppVersion={#AppVersion}
AppPublisher=Gui
DefaultDirName={autopf}\{#MyAppName}
DefaultGroupName={#MyAppName}
DisableProgramGroupPage=yes
PrivilegesRequired=lowest
OutputDir=.
OutputBaseFilename=SnowDownloader-Setup
SetupIconFile=snow.ico
UninstallDisplayIcon={app}\SnowDownloader.exe
Compression=lzma2
SolidCompression=yes
WizardStyle=modern

[Languages]
Name: "brazilianportuguese"; MessagesFile: "compiler:Languages\BrazilianPortuguese.isl"

[Tasks]
Name: "desktopicon"; Description: "Criar um atalho na Área de Trabalho"; Flags: unchecked

[Files]
Source: "SnowDownloader.exe"; DestDir: "{app}"; Flags: ignoreversion

[Icons]
Name: "{autoprograms}\{#MyAppName}"; Filename: "{app}\SnowDownloader.exe"
Name: "{autodesktop}\{#MyAppName}"; Filename: "{app}\SnowDownloader.exe"; Tasks: desktopicon

[Run]
Filename: "{app}\SnowDownloader.exe"; Description: "Abrir o SnowDownloader"; Flags: nowait postinstall skipifsilent

[UninstallDelete]
Type: filesandordirs; Name: "{app}\tools"
Type: files; Name: "{app}\SnowDownloader.exe.old"
Type: files; Name: "{app}\SnowDownloader.exe.new"
