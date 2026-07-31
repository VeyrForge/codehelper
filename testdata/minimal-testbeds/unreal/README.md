# Unreal-lite bed

Detect: `.uproject` + `EngineAssociation` (profile `project_type=unreal`).
C++ densify: `Source/MyGame/` with UCLASS/UFUNCTION/UPROPERTY/GENERATED_BODY,
`AMyGameCharacter` → `UHealthComponent` via CreateDefaultSubobject/Cast reads,
header method decls + out-of-line definitions.

Blueprint lite: `Config/DefaultEngine.ini` soft maps + ActiveClassRedirects
(searchable text). Binary `.uasset` is not indexed.

No Engine / UBT download required.
