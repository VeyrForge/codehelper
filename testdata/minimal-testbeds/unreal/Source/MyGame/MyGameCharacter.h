// Unreal-lite stub — no engine download. UE macros + component graph for agents.
#pragma once

#include "CoreMinimal.h"
#include "GameFramework/Character.h"
#include "HealthComponent.h"
#include "MyGameCharacter.generated.h"

UCLASS(BlueprintType)
class AMyGameCharacter : public ACharacter
{
	GENERATED_BODY()

public:
	AMyGameCharacter();

	virtual void BeginPlay() override;

	UFUNCTION(BlueprintCallable, Category = "Combat")
	void TakeDamage(float Amount);

	UPROPERTY(EditAnywhere, BlueprintReadWrite, Category = "Combat")
	float Health = 100.f;

	UPROPERTY(VisibleAnywhere, BlueprintReadOnly, Category = "Combat")
	UHealthComponent* HealthComp;
};
