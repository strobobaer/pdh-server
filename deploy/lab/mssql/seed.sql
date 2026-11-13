-- Beispieldaten für das Labor "Fremd". Mehrfach ausführbar.
IF DB_ID('Fremd') IS NULL CREATE DATABASE Fremd;
GO
USE Fremd;
GO
IF OBJECT_ID('dbo.Meldungen') IS NULL
CREATE TABLE dbo.Meldungen (
  MeldungNr INT IDENTITY(1,1) PRIMARY KEY,
  Titel NVARCHAR(200) NOT NULL,
  Beschreibung NVARCHAR(MAX) NULL,
  Prio NVARCHAR(10) NULL,
  Zustand NVARCHAR(20) NOT NULL,
  Faellig DATETIME2 NULL,
  Geaendert DATETIME2 NOT NULL DEFAULT SYSUTCDATETIME(),
  RV ROWVERSION
);
GO
IF OBJECT_ID('dbo.MeldungKommentare') IS NULL
CREATE TABLE dbo.MeldungKommentare (
  Id INT IDENTITY(1,1) PRIMARY KEY,
  MeldungNr INT NOT NULL,
  Text NVARCHAR(MAX) NULL,
  Verfasser NVARCHAR(100) NULL,
  Zeit DATETIME2 NULL
);
GO
IF OBJECT_ID('dbo.MeldungenGuid') IS NULL
CREATE TABLE dbo.MeldungenGuid (
  Id UNIQUEIDENTIFIER NOT NULL,
  Werk NVARCHAR(10) NOT NULL,
  Titel NVARCHAR(200) NOT NULL,
  Zustand NVARCHAR(20) NOT NULL,
  PRIMARY KEY (Id, Werk)
);
GO
CREATE OR ALTER TRIGGER dbo.trg_Meldungen_Geaendert ON dbo.Meldungen
AFTER UPDATE AS
BEGIN
  SET NOCOUNT ON;
  IF TRIGGER_NESTLEVEL() > 1 RETURN;
  UPDATE m SET Geaendert = SYSUTCDATETIME()
  FROM dbo.Meldungen m JOIN inserted i ON i.MeldungNr = m.MeldungNr;
END;
GO
IF NOT EXISTS (SELECT 1 FROM dbo.Meldungen)
INSERT INTO dbo.Meldungen (Titel, Beschreibung, Prio, Zustand, Faellig) VALUES
 (N'Pumpe 3 tropft', N'Leckage am Dichtring', N'hoch', N'offen', DATEADD(day, 2, SYSUTCDATETIME())),
 (N'Förderband steht', N'Motor springt nicht an', N'hoch', N'in Arbeit', DATEADD(day, 1, SYSUTCDATETIME())),
 (N'Lampe Halle 2 defekt', NULL, N'niedrig', N'offen', NULL),
 (N'Tür Lager klemmt', N'Scharnier nachziehen', N'mittel', N'offen', DATEADD(day, 7, SYSUTCDATETIME())),
 (N'Druckluft pfeift', N'Schlauch an Station 5', N'mittel', N'erledigt', NULL);
GO
IF NOT EXISTS (SELECT 1 FROM dbo.MeldungKommentare)
INSERT INTO dbo.MeldungKommentare (MeldungNr, Text, Verfasser, Zeit) VALUES
 (1, N'Dichtung bestellt', N'Müller', SYSUTCDATETIME());
GO
IF NOT EXISTS (SELECT 1 FROM dbo.MeldungenGuid)
INSERT INTO dbo.MeldungenGuid (Id, Werk, Titel, Zustand) VALUES
 (NEWID(), N'W1', N'Sensor defekt', N'offen'),
 (NEWID(), N'W2', N'Filter wechseln', N'offen');
GO
IF SUSER_ID('pdh_sync') IS NULL
  CREATE LOGIN pdh_sync WITH PASSWORD = 'Lab-Sync-Kennwort-2026!', CHECK_POLICY = OFF;
GO
IF USER_ID('pdh_sync') IS NULL CREATE USER pdh_sync FOR LOGIN pdh_sync;
GO
GRANT SELECT ON dbo.Meldungen TO pdh_sync;
GRANT SELECT ON dbo.MeldungKommentare TO pdh_sync;
GRANT SELECT ON dbo.MeldungenGuid TO pdh_sync;
GRANT UPDATE ON dbo.Meldungen TO pdh_sync;
GRANT UPDATE ON dbo.MeldungenGuid TO pdh_sync;
GRANT INSERT ON dbo.MeldungKommentare TO pdh_sync;
GO
